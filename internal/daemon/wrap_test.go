// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package daemon

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/elephaant/shiplino/internal/spool"
	"github.com/elephaant/shiplino/pkg/adapters/aider"
	"github.com/elephaant/shiplino/pkg/adapters/wrap"
	"github.com/elephaant/shiplino/pkg/model"
)

// spoolWrap writes an envelope the way `shiplino wrap` does.
func (e *env) spoolWrap(event string, p wrap.Payload) {
	e.t.Helper()
	b, _ := json.Marshal(p)
	now := time.Now()
	env := spool.Envelope{ID: model.NewULID(now), Agent: wrap.Name, Event: event, TS: now.UnixNano(), PID: os.Getpid()}
	if err := spool.Write(spool.Dir(e.home), p.SessionID, env, b); err != nil {
		e.t.Fatal(err)
	}
}

// A wrapped Aider run: its repo is watched like a hook session's, so the
// user's commit of Aider's edit links to the session.
func TestWrappedAiderSessionAndCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	withHome(t)
	e := newEnv(t)
	repo := filepath.Join(t.TempDir(), "app")
	os.MkdirAll(filepath.Join(repo, "src"), 0o755)
	gitIn(t, repo, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(repo, "README.md"), []byte("x\n"), 0o644)
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-q", "-m", "init")

	id := model.NewULID(time.Now())
	sid := "aider:" + id
	base := wrap.Payload{SessionID: id, Agent: aider.Name, CWD: repo}
	start := base
	start.Command = "aider --model sonnet"
	e.spoolWrap(wrap.EventStart, start)
	e.poll()
	if s := e.session(sid); s.Status != "running" || s.Agent != "aider" || s.Branch != "main" {
		t.Fatalf("after start: %+v", s)
	}
	if e.d.git.Watching() != 1 {
		t.Fatalf("watching %d worktrees", e.d.git.Watching())
	}

	hist := base
	hist.Root = repo
	for i, l := range []string{"#### add auth  ", "> Tokens: 1.2k sent, 200 received. Cost: $0.03 message, $0.03 session.  ", "> Applied edit to src/auth.py  "} {
		hist.Lines = append(hist.Lines, aider.Line{Offset: int64(100 * i), Len: len(l) + 1, Text: l})
	}
	e.spoolWrap(wrap.EventHistory, hist)
	e.poll()
	s := e.session(sid)
	if s.Status != "review" || s.Turns != 1 || s.CostSource != "reported" || s.BestCostUSD != 0.03 || len(s.Files) != 1 {
		t.Fatalf("after history: status=%s turns=%d cost=%v/%s files=%v", s.Status, s.Turns, s.BestCostUSD, s.CostSource, s.Files)
	}

	os.WriteFile(filepath.Join(repo, "src", "auth.py"), []byte("def login(): pass\n"), 0o644)
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-q", "-m", "feat: auth")
	e.poll()
	s = e.session(sid)
	if s.Status != "done" || len(s.Links) != 1 || s.Links[0].Message != "feat: auth" {
		t.Fatalf("after commit: status=%s links=%+v", s.Status, s.Links)
	}

	code := 0
	end := base
	end.ExitCode, end.DurationMS = &code, 1000
	e.spoolWrap(wrap.EventEnd, end)
	e.poll()
	if s := e.session(sid); s.EndedAt.IsZero() || s.Status != "done" {
		t.Fatalf("after end: %+v", s)
	}
}
