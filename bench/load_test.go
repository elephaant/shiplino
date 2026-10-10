// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

// Package bench holds end-to-end load tests: hook payloads go through the
// real shim into the spool, the daemon ingests them, and a WebSocket
// client measures when they reach the UI.
//
//	go test ./bench              # short variant, a few seconds (CI)
//	go test ./bench -v -full     # 50 sessions × 4 subagents, checks the targets (make bench)
//
// Set SHIPLINO_BENCH_BIN to a built binary to spawn `shiplino hook` for
// every payload instead of calling the shim in-process.
package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/elephaant/shiplino/internal/api"
	"github.com/elephaant/shiplino/internal/daemon"
	"github.com/elephaant/shiplino/internal/shim"
	"github.com/elephaant/shiplino/internal/store"
	"github.com/elephaant/shiplino/pkg/engine"
	"github.com/elephaant/shiplino/pkg/projects"
)

var full = flag.Bool("full", false, "run the full-size load test and enforce the performance targets")

// Targets enforced by the full run.
const (
	targetHookToUI   = 500 * time.Millisecond // p95
	targetIngest     = 20000                  // events/s
	targetBoardQuery = 30 * time.Millisecond  // p95
)

const token = "bench-token-0123456789abcdef0123456789abcdef0123456789abcdef"

// shape is the simulated workload: every subagent makes `rate` tool calls
// per second (a PreToolUse and a PostToolUse hook each) for `dur`.
type shape struct {
	sessions, subagents, rate, projects int
	dur                                 time.Duration
}

func load() shape {
	if *full {
		return shape{sessions: 50, subagents: 4, rate: 10, projects: 5, dur: 20 * time.Second}
	}
	return shape{sessions: 10, subagents: 4, rate: 10, projects: 2, dur: 2 * time.Second}
}

// hook runs one hook invocation through the real shim.
func hook(t testing.TB, payload string) {
	if bin := os.Getenv("SHIPLINO_BENCH_BIN"); bin != "" {
		cmd := exec.Command(bin, "hook", "--agent", "claude-code")
		cmd.Stdin = strings.NewReader(payload)
		if out, err := cmd.CombinedOutput(); err != nil || len(out) > 0 {
			t.Errorf("hook: %v %q", err, out)
		}
		return
	}
	shim.Run([]string{"--agent", "claude-code"}, strings.NewReader(payload))
}

// hermetic points Shiplino and the agents' home folders at temp dirs.
func hermetic(t testing.TB) string {
	home := t.TempDir()
	t.Setenv("SHIPLINO_HOME", home)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	return home
}

// tracker measures hook → visible latency. A hook's start time is queued
// under its session before the shim runs; when a session update with
// LastEventAt ≥ that time is seen, the event is visible. Each actor sends
// sequentially, so this matches events exactly.
type tracker struct {
	mu      sync.Mutex
	pending map[string][]time.Time
	lat     []time.Duration
}

func newTracker() *tracker { return &tracker{pending: map[string][]time.Time{}} }

func (t *tracker) sent(id string, at time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pending[id] = append(t.pending[id], at)
}

func (t *tracker) seen(id string, upTo time.Time) {
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	q := t.pending[id]
	i := 0
	for i < len(q) && !q[i].After(upTo) {
		t.lat = append(t.lat, now.Sub(q[i]))
		i++
	}
	t.pending[id] = q[i:]
}

func (t *tracker) left() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for _, q := range t.pending {
		n += len(q)
	}
	return n
}

// oldest describes one event not seen yet, for failure messages.
func (t *tracker) oldest() (id string, at time.Time, n int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, q := range t.pending {
		if len(q) > 0 {
			return k, q[0], len(q)
		}
	}
	return "", time.Time{}, 0
}

func (t *tracker) durations() []time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.lat)
}

// pct returns the p-th percentile (0–100) of ds.
func pct(ds []time.Duration, p float64) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	s := slices.Clone(ds)
	slices.Sort(s)
	return s[min(len(s)-1, int(float64(len(s))*p/100))]
}

func summary(ds []time.Duration) string {
	return fmt.Sprintf("n=%d p50=%s p95=%s p99=%s max=%s", len(ds),
		pct(ds, 50).Round(time.Microsecond), pct(ds, 95).Round(time.Microsecond),
		pct(ds, 99).Round(time.Microsecond), pct(ds, 100).Round(time.Microsecond))
}

// rig is a daemon with the API in front of it, as `shiplino daemon` runs.
type rig struct {
	home    string
	st      *store.Store
	d       *daemon.Daemon
	url     string
	mu      sync.Mutex
	commits []time.Duration
	events  int
	logs    syncBuffer // the daemon's log, shown when a test fails
}

// diagnose describes a stuck daemon: counters, each spool file's size
// against its stored cursor, the daemon's log and every goroutine.
func (r *rig) diagnose() string {
	var b strings.Builder
	fmt.Fprintf(&b, "stats %+v\n", r.d.Stats())
	// The store may be what's stuck: don't wait on it for long.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cursors, err := r.st.Cursors(ctx)
	if err != nil {
		fmt.Fprintf(&b, "cursors: %v\n", err)
	}
	files, _ := filepath.Glob(filepath.Join(r.home, "spool", "*", "*"))
	for _, f := range files {
		rel, _ := filepath.Rel(filepath.Join(r.home, "spool"), f)
		fi, err := os.Stat(f)
		if err != nil {
			fmt.Fprintf(&b, "  %s: %v\n", rel, err)
			continue
		}
		fmt.Fprintf(&b, "  %s: size %d, cursor %d\n", rel, fi.Size(), cursors["spool/"+filepath.ToSlash(rel)])
	}
	fmt.Fprintf(&b, "daemon log:\n%s\n", r.logs.String())
	buf := make([]byte, 4<<20)
	fmt.Fprintf(&b, "goroutines:\n%s", buf[:runtime.Stack(buf, true)])
	return b.String()
}

// syncBuffer is a bytes.Buffer safe for concurrent writes.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newRig(t testing.TB, onChange func([]*engine.Session)) *rig {
	t.Helper()
	r := &rig{home: hermetic(t)}
	st, err := store.Open(filepath.Join(r.home, "data", "shiplino.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	d, err := daemon.New(context.Background(), r.home, st, log.New(&r.logs, "", log.Lmicroseconds))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	hub := api.NewHub()
	go hub.Run(ctx)
	d.OnChange = func(list []*engine.Session) {
		if onChange != nil {
			onChange(list)
		}
		hub.Publish(list)
	}
	d.OnCommit = func(n int, took time.Duration) {
		r.mu.Lock()
		r.commits = append(r.commits, took)
		r.events += n
		r.mu.Unlock()
	}
	srv := httptest.NewServer(api.New(st, hub, token, "bench", log.New(io.Discard, "", 0)).Handler())
	t.Cleanup(srv.Close)
	r.st, r.d, r.url = st, d, srv.URL
	return r
}

func (r *rig) commitStats() ([]time.Duration, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.commits), r.events
}

// live connects to the WebSocket feed and reports every session update.
func (r *rig) live(t testing.TB, ctx context.Context, fn func(*engine.Session)) {
	t.Helper()
	h := http.Header{}
	h.Set("Authorization", "Bearer "+token)
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(r.url, "http")+"/api/v1/live", &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		t.Fatal(err)
	}
	conn.SetReadLimit(1 << 20)
	go func() {
		defer conn.CloseNow()
		for {
			_, b, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var m api.Message
			if json.Unmarshal(b, &m) == nil && m.Session != nil {
				fn(m.Session)
			}
		}
	}()
}

func (r *rig) get(t testing.TB, path string) []byte {
	t.Helper()
	req, _ := http.NewRequest("GET", r.url+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s: %d %s", path, resp.StatusCode, b)
	}
	return b
}

// waitFor polls cond until it holds (true) or timeout passes (false).
func waitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
	return true
}

// payload is a Claude Code hook payload. Strings are quoted with %q, which
// is valid JSON for paths too (Windows backslashes).
func payload(session, cwd, event, extra string) string {
	return fmt.Sprintf(`{"session_id":%q,"cwd":%q,"hook_event_name":%q%s}`, session, cwd, event, extra)
}

// TestLoad runs concurrent sessions with subagents through the shim and
// the running daemon, and measures hook → OnChange and hook → WebSocket
// latency, per-commit time, and board queries on the result.
func TestLoad(t *testing.T) {
	sh := load()
	byChange, byUI := newTracker(), newTracker()
	r := newRig(t, func(list []*engine.Session) {
		for _, s := range list {
			if s.ParentID != "" {
				byChange.seen(s.ID, s.LastEventAt)
			}
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.live(t, ctx, func(s *engine.Session) {
		if s.ParentID != "" {
			byUI.seen(s.ID, s.LastEventAt)
		}
	})
	go r.d.Run(ctx)

	cwds := make([]string, sh.projects)
	for i := range cwds {
		cwds[i] = filepath.Join(t.TempDir(), fmt.Sprintf("project-%d", i))
		if err := os.MkdirAll(cwds[i], 0o700); err != nil {
			t.Fatal(err)
		}
	}
	var lines atomic.Int64
	send := func(p string) {
		hook(t, p)
		lines.Add(1)
	}
	calls := map[string]int{} // child session id → tool calls made
	var callsMu sync.Mutex

	start := time.Now()
	var wg sync.WaitGroup
	for i := range sh.sessions {
		wg.Go(func() {
			sess, cwd := fmt.Sprintf("load-%03d", i), cwds[i%len(cwds)]
			send(payload(sess, cwd, "SessionStart", `,"source":"startup","model":"claude-sonnet-5-5"`))
			send(payload(sess, cwd, "UserPromptSubmit", `,"prompt_id":"p-1","prompt":"run the load test"`))
			var subs sync.WaitGroup
			for a := range sh.subagents {
				agent := fmt.Sprintf("ag-%d", a)
				id := "claude-code:" + sess + "/sub:" + agent
				send(payload(sess, cwd, "SubagentStart", fmt.Sprintf(`,"agent_id":%q,"agent_type":"worker"`, agent)))
				subs.Go(func() {
					every := time.Second / time.Duration(sh.rate)
					time.Sleep(time.Duration(rand.Int64N(int64(every)))) // don't fire in lockstep
					tick := time.NewTicker(every)
					defer tick.Stop()
					n := 0
					for time.Since(start) < sh.dur {
						tu := fmt.Sprintf("tu-%s-%s-%d", sess, agent, n)
						input := fmt.Sprintf(`,"agent_id":%q,"agent_type":"worker","tool_name":"Read","tool_input":{"file_path":%q},"tool_use_id":%q`, agent, filepath.Join(cwd, "src", fmt.Sprintf("f%d.ts", n%7)), tu)
						for _, ev := range []string{"PreToolUse", "PostToolUse"} {
							now := time.Now()
							byChange.sent(id, now)
							byUI.sent(id, now)
							extra := input
							if ev == "PostToolUse" {
								extra += `,"tool_response":{},"duration_ms":3`
							}
							send(payload(sess, cwd, ev, extra))
						}
						n++
						<-tick.C
					}
					callsMu.Lock()
					calls[id] = n
					callsMu.Unlock()
					send(payload(sess, cwd, "SubagentStop", fmt.Sprintf(`,"agent_id":%q,"agent_type":"worker","last_assistant_message":"done"`, agent)))
				})
			}
			subs.Wait()
			send(payload(sess, cwd, "Stop", `,"prompt_id":"p-1","last_assistant_message":"done"`))
			send(payload(sess, cwd, "SessionEnd", `,"reason":"prompt_input_exit"`))
		})
	}
	wg.Wait()
	sent := lines.Load()
	writeTime := time.Since(start)

	if !waitFor(60*time.Second, func() bool { return r.d.Stats().Lines >= sent }) {
		t.Fatalf("read %d of %d lines\n%s", r.d.Stats().Lines, sent, r.diagnose())
	}
	drained := time.Since(start)
	explain := func(tr *tracker) {
		id, at, n := tr.oldest()
		s, err := r.st.Session(context.Background(), id)
		var last time.Time
		if s != nil {
			last = s.LastEventAt
		}
		t.Fatalf("%d events not seen; %s has %d, the first sent at %d, stored last_event_at %d (%v)\n%s",
			tr.left(), id, n, at.UnixNano(), last.UnixNano(), err, r.diagnose())
	}
	if !waitFor(10*time.Second, func() bool { return byChange.left() == 0 }) {
		explain(byChange)
	}
	if !waitFor(10*time.Second, func() bool { return byUI.left() == 0 }) {
		explain(byUI)
	}

	// Correctness: nothing lost, nothing counted twice.
	st := r.d.Stats()
	if st.Bad != 0 || st.Unknown != 0 {
		t.Errorf("stats: %+v", st)
	}
	// Lines are counted when read; the writer commits them a moment later,
	// so wait for the stored result rather than checking once.
	settled := func(id string, n int) (*engine.Session, error, bool) {
		s, err := r.st.Session(context.Background(), id)
		return s, err, err == nil && s != nil && s.ToolCalls == n && s.Status == engine.StatusDone
	}
	waitFor(10*time.Second, func() bool {
		for id, n := range calls {
			if _, _, ok := settled(id, n); !ok {
				return false
			}
		}
		return true
	})
	for id, n := range calls {
		if s, err, ok := settled(id, n); !ok {
			t.Fatalf("%s: want %d tool calls, done; got %+v %v\n%s", id, n, s, err, r.diagnose())
		}
	}

	commits, events := r.commitStats()
	t.Logf("load: %d sessions × %d subagents × %d tool calls/s for %s", sh.sessions, sh.subagents, sh.rate, sh.dur)
	t.Logf("hooks: %d lines in %s (%.0f/s), all stored after %s; %d events", sent, writeTime.Round(time.Millisecond),
		float64(sent)/writeTime.Seconds(), drained.Round(time.Millisecond), st.Events)
	t.Logf("hook → OnChange:   %s", summary(byChange.durations()))
	t.Logf("hook → WebSocket:  %s", summary(byUI.durations()))
	t.Logf("commits: %s, %.1f events/commit", summary(commits), float64(events)/float64(max(1, len(commits))))

	board := boardQueries(t, r, 20)
	t.Logf("board query:       %s", summary(board))

	if *full {
		if p := pct(byUI.durations(), 95); p > targetHookToUI {
			t.Errorf("hook → UI p95 = %s, target < %s", p, targetHookToUI)
		}
		if p := pct(board, 95); p > targetBoardQuery {
			t.Errorf("board query p95 = %s, target < %s", p, targetBoardQuery)
		}
	}
}

// boardQueries times the board endpoint for every project.
func boardQueries(t testing.TB, r *rig, rounds int) []time.Duration {
	t.Helper()
	list, err := r.st.Projects(context.Background())
	if err != nil || len(list) == 0 {
		t.Fatalf("projects: %v %v", list, err)
	}
	var out []time.Duration
	for range rounds {
		for _, p := range list {
			t0 := time.Now()
			r.get(t, "/api/v1/projects/"+url.PathEscape(p.ID)+"/board?sprint=all")
			out = append(out, time.Since(t0))
		}
	}
	return out
}

// TestIngestThroughput writes a backlog to the spool, then times one pass
// of the daemon over it: raw ingest capacity, not limited by hook rate.
func TestIngestThroughput(t *testing.T) {
	sessions, perSession := 10, 200 // tool calls per session, 2 lines each
	if *full {
		sessions, perSession = 200, 500
	}
	r := newRig(t, nil)
	cwd := t.TempDir()
	var wg sync.WaitGroup
	for i := range sessions {
		wg.Go(func() {
			sess := fmt.Sprintf("ingest-%03d", i)
			hook(t, payload(sess, cwd, "SessionStart", `,"source":"startup"`))
			for n := range perSession {
				input := fmt.Sprintf(`,"agent_id":"ag-%d","tool_name":"Bash","tool_input":{"command":"go test ./pkg/%d"},"tool_use_id":"tu-%d"`, n%4, n, n)
				hook(t, payload(sess, cwd, "PreToolUse", input))
				hook(t, payload(sess, cwd, "PostToolUse", input+`,"tool_response":{"stdout":"ok"},"duration_ms":40`))
			}
		})
	}
	wg.Wait()

	t0 := time.Now()
	if err := r.d.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	took := time.Since(t0)
	st := r.d.Stats()
	if want := int64(sessions * (1 + 2*perSession)); st.Lines != want || st.Bad != 0 {
		t.Fatalf("lines = %d (want %d), stats %+v", st.Lines, want, st)
	}
	rate := float64(st.Events) / took.Seconds()
	commits, events := r.commitStats()
	t.Logf("ingest: %d lines → %d events in %s: %.0f events/s", st.Lines, st.Events, took.Round(time.Millisecond), rate)
	t.Logf("commits: %s, %.1f events/commit", summary(commits), float64(events)/float64(max(1, len(commits))))
	db := filepath.Join(r.home, "data", "shiplino.db")
	t.Logf("database %.0f MB + WAL %.0f MB: %.1f KB per event", mb(db), mb(db+"-wal"), (mb(db)+mb(db+"-wal"))*1024/float64(max(1, st.Events)))
	if *full && rate < targetIngest {
		t.Errorf("ingest = %.0f events/s, target > %d", rate, targetIngest)
	}
}

// mb is a file's size in megabytes (0 if it doesn't exist).
func mb(path string) float64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return float64(fi.Size()) / (1 << 20)
}

// TestBoardQueryWithHistory times the board of a project with a long
// history: finished work from past sprints next to the current one.
func TestBoardQueryWithHistory(t *testing.T) {
	roots := 200
	if *full {
		roots = 2000
	}
	r := newRig(t, nil)
	p := seedHistory(t, r, roots)
	board := "/api/v1/projects/" + url.PathEscape(p.ID) + "/board"
	var cur, all []time.Duration
	for range 20 {
		t0 := time.Now()
		r.get(t, board)
		cur = append(cur, time.Since(t0))
	}
	for range 5 {
		t0 := time.Now()
		r.get(t, board+"?sprint=all")
		all = append(all, time.Since(t0))
	}
	t.Logf("board, %d root sessions × 4 subagents, current sprint: %s", roots, summary(cur))
	t.Logf("board, %d root sessions × 4 subagents, all sprints:    %s", roots, summary(all))
	if *full {
		if p := pct(cur, 95); p > targetBoardQuery {
			t.Errorf("board (current sprint) p95 = %s, target < %s", p, targetBoardQuery)
		}
	}
}

// seedHistory stores a project with `roots` finished root sessions, one
// an hour going back, each with 4 subagents.
func seedHistory(t testing.TB, r *rig, roots int) projects.Project {
	t.Helper()
	ctx := context.Background()
	p := projects.Project{ID: "proj-history", Name: "history", Kind: "folder"}
	now := time.Now().UTC()
	tx, err := r.st.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := range roots {
		at := now.Add(-time.Duration(i) * time.Hour)
		root := &engine.Session{ID: fmt.Sprintf("claude-code:h-%05d", i), Agent: "claude-code", Status: engine.StatusDone,
			ProjectID: p.ID, StartedAt: at, LastEventAt: at, EndedAt: at, Title: "Refactor the parser", ToolCalls: 40,
			Files: []string{"/home/dev/demo/a.go", "/home/dev/demo/b.go"}, BestCostUSD: 0.42}
		root.RootID = root.ID
		if err := tx.PutSession(ctx, root); err != nil {
			t.Fatal(err)
		}
		for a := range 4 {
			child := &engine.Session{ID: fmt.Sprintf("%s/sub:ag-%d", root.ID, a), Agent: "claude-code", ParentID: root.ID, RootID: root.ID,
				Status: engine.StatusDone, ProjectID: p.ID, StartedAt: at, LastEventAt: at, ToolCalls: 10}
			if err := tx.PutSession(ctx, child); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tx.PutProject(ctx, p, now.Add(-time.Duration(roots)*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return p
}
