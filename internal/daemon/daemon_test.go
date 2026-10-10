// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

package daemon

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/elephaant/shiplino/internal/shim"
	"github.com/elephaant/shiplino/internal/spool"
	"github.com/elephaant/shiplino/internal/store"
	"github.com/elephaant/shiplino/pkg/engine"

	_ "modernc.org/sqlite"
)

var ctx = context.Background()

const fixture = "../../pkg/adapters/claudecode/testdata/2.1/session.jsonl"

type env struct {
	t    *testing.T
	home string
	db   string
	st   *store.Store
	d    *Daemon
}

func newEnv(t *testing.T) *env {
	t.Helper()
	home := t.TempDir()
	t.Setenv("SHIPLINO_HOME", home)
	e := &env{t: t, home: home, db: filepath.Join(home, "data", "shiplino.db")}
	e.restart()
	t.Cleanup(func() { e.st.Close() })
	return e
}

// restart simulates a daemon restart: fresh store handle and daemon.
func (e *env) restart() {
	e.t.Helper()
	if e.st != nil {
		e.st.Close()
	}
	st, err := store.Open(e.db)
	if err != nil {
		e.t.Fatal(err)
	}
	d, err := New(ctx, e.home, st, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	e.st, e.d = st, d
}

func (e *env) hook(payload string) {
	shim.Run([]string{"--agent", "claude-code"}, strings.NewReader(payload))
}

func (e *env) hookFixture() {
	e.t.Helper()
	f, err := os.Open(fixture)
	if err != nil {
		e.t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		e.hook(sc.Text())
	}
}

func (e *env) poll() {
	e.t.Helper()
	if err := e.d.Poll(ctx); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) eventCount() int {
	e.t.Helper()
	db, err := sql.Open("sqlite", e.db)
	if err != nil {
		e.t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) session(id string) *engine.Session {
	e.t.Helper()
	s, err := e.st.Session(ctx, id)
	if err != nil || s == nil {
		e.t.Fatalf("session %s: %v %v", id, s, err)
	}
	return s
}

const sid = "claude-code:sess-0001"

func TestEndToEndFromHookToSession(t *testing.T) {
	e := newEnv(t)
	e.hookFixture()
	e.poll()

	s := e.session(sid)
	// Claude Code reported its own title, so it wins over the first prompt.
	if s.Title != "Greeting fix" || s.TitleSource != "agent" || s.Model != "claude-sonnet-5-5" || s.Turns != 2 {
		t.Errorf("session: %+v", s)
	}
	if s.Status != engine.StatusDone { // a new turn started, then the session ended
		t.Errorf("status = %s", s.Status)
	}
	// Lines: greet.ts estimated +2 −1, NOTES.md created +3, app.ts from the agent's own diff +3 −1.
	if s.FilesChanged() != 3 || s.LinesAdded != 8 || s.LinesRemoved != 2 || s.ToolErrors != 1 || s.EndedAt.IsZero() {
		t.Errorf("counters: files=%v +%d -%d errors=%d ended=%v", s.Files, s.LinesAdded, s.LinesRemoved, s.ToolErrors, s.EndedAt)
	}
	if len(s.Links) != 1 || s.Links[0].Number != 12 {
		t.Errorf("links: %+v", s.Links)
	}
	child := e.session(sid + "/sub:ag-7")
	if child.ParentID != sid || child.Status != engine.StatusDone || child.ToolCalls != 1 {
		t.Errorf("child: %+v", child)
	}
	st := e.d.Stats()
	if st.Lines != 25 || st.Bad != 0 || st.Unknown != 0 || st.Events != int64(e.eventCount()) {
		t.Errorf("stats = %+v, events in db = %d", st, e.eventCount())
	}
}

func TestRestartNeitherLosesNorDuplicates(t *testing.T) {
	e := newEnv(t)
	e.hook(`{"session_id":"s2","hook_event_name":"UserPromptSubmit","prompt":"hello"}`)
	e.poll()
	n := e.eventCount()

	e.restart()
	e.poll()
	if got := e.eventCount(); got != n {
		t.Fatalf("events after restart = %d, want %d", got, n)
	}
	e.hook(`{"session_id":"s2","hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"file_path":"/x/a.go"},"tool_use_id":"t1"}`)
	e.poll()
	s := e.session("claude-code:s2")
	if s.Turns != 1 || s.ToolCalls != 1 || s.NowDoing != "Reading a.go" {
		t.Fatalf("after restart: %+v", s)
	}
}

func TestReplayFromZeroIsIdempotent(t *testing.T) {
	e := newEnv(t)
	e.hookFixture()
	e.poll()
	before := e.session(sid)
	n := e.eventCount()

	// Forget every cursor, as if offsets were lost: lines are re-read but
	// dedup must stop them from being stored or applied twice.
	db, _ := sql.Open("sqlite", e.db)
	db.Exec(`DELETE FROM cursors`)
	db.Close()
	e.restart()
	e.poll()

	after := e.session(sid)
	if e.eventCount() != n || after.ToolCalls != before.ToolCalls || after.Turns != before.Turns || after.LinesAdded != before.LinesAdded {
		t.Fatalf("replay changed state: events %d→%d, before %+v after %+v", n, e.eventCount(), before, after)
	}
}

func TestPartialLineWaits(t *testing.T) {
	e := newEnv(t)
	path := spool.SessionFile(spool.Dir(e.home), "claude-code", "s3")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	full := `{"id":"01JTEST000000000000000000A","a":"claude-code","t":1791367351512000000,"pid":1,"p":{"session_id":"s3","hook_event_name":"SessionStart","source":"startup"}}` + "\n"
	half := len(full) / 2
	os.WriteFile(path, []byte(full[:half]), 0o600)
	e.poll()
	if e.eventCount() != 0 {
		t.Fatal("partial line was consumed")
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(full[half:])
	f.Close()
	e.poll()
	if e.eventCount() != 1 {
		t.Fatalf("events = %d after completing the line", e.eventCount())
	}
}

func TestBadAndUnknownLinesAreCounted(t *testing.T) {
	e := newEnv(t)
	shim.Run([]string{"--agent", "claude-code"}, strings.NewReader("not json"))
	shim.Run([]string{"--agent", "mystery-agent"}, strings.NewReader(`{"session_id":"m1"}`))
	e.hook(`{"session_id":"s4","hook_event_name":"FutureEvent"}`)
	e.hook(`{"session_id":"s4","hook_event_name":"Stop"}`)
	e.poll()
	st := e.d.Stats()
	if st.Bad != 1 || st.Unknown != 2 || st.Events != 1 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestBlobPayloadsAreProcessedAndDeleted(t *testing.T) {
	e := newEnv(t)
	big := `{"session_id":"s5","hook_event_name":"UserPromptSubmit","prompt":"` + strings.Repeat("long ", 2000) + `"}`
	e.hook(big)
	e.poll()
	s := e.session("claude-code:s5")
	if s.Turns != 1 || !strings.HasPrefix(s.Title, "long long") {
		t.Fatalf("session from blob: %+v", s)
	}
	blobs, _ := os.ReadDir(filepath.Join(spool.Dir(e.home), "blobs"))
	if len(blobs) != 0 {
		t.Fatalf("blobs left behind: %v", blobs)
	}
}

func TestIdleSpoolFilesAreReaped(t *testing.T) {
	e := newEnv(t)
	e.d.reapAfter = 0
	e.hook(`{"session_id":"s6","hook_event_name":"UserPromptSubmit","prompt":"one"}`)
	path := spool.SessionFile(spool.Dir(e.home), "claude-code", "s6")
	e.poll()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("spool file not reaped: %v", err)
	}
	if cur, _ := e.st.Cursors(ctx); len(cur) != 0 {
		t.Fatalf("cursor not removed: %v", cur)
	}
	// The session keeps going in a fresh file.
	e.hook(`{"session_id":"s6","hook_event_name":"UserPromptSubmit","prompt":"two"}`)
	e.poll()
	if s := e.session("claude-code:s6"); s.Turns != 2 {
		t.Fatalf("turns = %d", s.Turns)
	}
}

func TestInterruptedReapIsFinished(t *testing.T) {
	e := newEnv(t)
	e.hook(`{"session_id":"s7","hook_event_name":"UserPromptSubmit","prompt":"hi"}`)
	path := spool.SessionFile(spool.Dir(e.home), "claude-code", "s7")
	// Crash right after the rename, before anything was processed.
	if err := os.Rename(path, path+".done"); err != nil {
		t.Fatal(err)
	}
	e.poll()
	if _, err := os.Stat(path + ".done"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(".done file left behind")
	}
	if s := e.session("claude-code:s7"); s.Turns != 1 {
		t.Fatalf("turns = %d", s.Turns)
	}
}

func TestOnChangeAndRun(t *testing.T) {
	e := newEnv(t)
	got := make(chan []*engine.Session, 10)
	e.d.OnChange = func(s []*engine.Session) { got <- s }
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error)
	go func() { done <- e.d.Run(runCtx) }()

	time.Sleep(50 * time.Millisecond) // let the watcher start
	e.hook(`{"session_id":"s8","hook_event_name":"SessionStart","source":"startup"}`)
	select {
	case list := <-got:
		if len(list) != 1 || list[0].ID != "claude-code:s8" {
			t.Fatalf("OnChange got %+v", list)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no change notification within 5s")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestLock(t *testing.T) {
	home := t.TempDir()
	l, err := AcquireLock(home)
	if err != nil {
		t.Fatal(err)
	}
	// Our own pid counts as stale for a second acquire in the same process,
	// so simulate another live owner with the parent process.
	os.WriteFile(filepath.Join(home, "daemon.lock"), []byte(strconv.Itoa(os.Getppid())), 0o600)
	if _, err := AcquireLock(home); !errors.Is(err, ErrLocked) {
		t.Fatalf("second acquire: %v", err)
	}
	// A lock from a dead process is taken over.
	os.WriteFile(filepath.Join(home, "daemon.lock"), []byte("999999999"), 0o600)
	l2, err := AcquireLock(home)
	if err != nil {
		t.Fatalf("stale lock not taken over: %v", err)
	}
	l2.Release()
	_ = l
}

func TestIdleSessionsAreMarked(t *testing.T) {
	e := newEnv(t)
	var changed []*engine.Session
	e.d.OnChange = func(l []*engine.Session) { changed = append(changed, l...) }
	e.hook(`{"session_id":"s9","hook_event_name":"UserPromptSubmit","prompt":"go"}`)
	e.poll()
	if s := e.session("claude-code:s9"); s.Status != engine.StatusRunning {
		t.Fatalf("status %s", s.Status)
	}
	changed = nil
	e.d.mu.Lock()
	err := e.d.checkIdle(ctx, time.Now().Add(engine.IdleAfter+time.Minute))
	e.d.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if s := e.session("claude-code:s9"); s.Status != engine.StatusIdle || len(changed) != 1 {
		t.Fatalf("after 31m: %s, %d changes", s.Status, len(changed))
	}
}
