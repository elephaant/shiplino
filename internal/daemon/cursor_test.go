package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/elephaant/shiplino/internal/shim"
	"github.com/elephaant/shiplino/pkg/model"
)

func TestCursorHooksEndToEnd(t *testing.T) {
	withHome(t)
	e := newEnv(t)
	lines, err := readLines("../../pkg/adapters/cursor/testdata/2026.09/hooks.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range lines {
		shim.Run([]string{"--agent", "cursor"}, strings.NewReader(l))
	}
	// Cursor also runs the Claude Code hook: that copy must be ignored.
	shim.Run([]string{"--agent", "claude-code"}, strings.NewReader(lines[2]))
	e.poll()

	s := e.session("cursor:cv-1")
	if s.Turns != 1 || s.ToolCalls != 6 || s.ToolErrors != 1 || s.LinesAdded != 2 || s.LinesRemoved != 1 {
		t.Fatalf("session: turns=%d tools=%d errs=%d +%d -%d", s.Turns, s.ToolCalls, s.ToolErrors, s.LinesAdded, s.LinesRemoved)
	}
	// A TodoWrite list, then a merge that completes one item and cancels one.
	if s.PlanTotal != 2 || s.PlanDone != 2 || len(s.PlanItems) != 3 || s.PlanItems[1].Text != "Fix roundTotal" {
		t.Fatalf("plan: %d/%d %+v", s.PlanDone, s.PlanTotal, s.PlanItems)
	}
	// The merge is stored with its counts (what sync sends).
	var merged model.Event
	e.st.EachEvent(ctx, func(ev model.Event) error {
		if ev.Data["plan_merge"] == true {
			merged = ev
		}
		return nil
	})
	if merged.Data["plan_total"] != 2.0 || merged.Data["plan_done"] != 2.0 {
		t.Fatalf("stored merge: %v", merged.Data)
	}
	if s.InputTokens != 1200 || s.Usage != "tokens" || s.CostUSD <= 0 || s.AgentVersion != "2026.09.02" {
		t.Fatalf("usage: in=%d cost=%v version=%q", s.InputTokens, s.CostUSD, s.AgentVersion)
	}
	sub := e.session("cursor:cv-1/sub:sa-9")
	if sub.ParentID != "cursor:cv-1" || sub.ToolCalls != 1 || sub.Status != "done" {
		t.Fatalf("subagent: %+v", sub)
	}
	if _, err := e.st.Session(ctx, "claude-code:cv-1"); err == nil {
		if s, _ := e.st.Session(ctx, "claude-code:cv-1"); s != nil {
			t.Fatal("Cursor-run Claude Code hook created a session")
		}
	}
}

// copyCursorTranscripts puts the transcript fixture under home as
// conversation conv, with its subagent.
func copyCursorTranscripts(t *testing.T, home, conv string) {
	t.Helper()
	src := "../../pkg/adapters/cursor/testdata/2026.09/projects/home-dev-shop/agent-transcripts/cv-7"
	dst := filepath.Join(home, ".cursor", "projects", "home-dev-shop", "agent-transcripts", conv)
	for from, to := range map[string]string{
		filepath.Join(src, "cv-7.jsonl"):              filepath.Join(dst, conv+".jsonl"),
		filepath.Join(src, "subagents", "sa-3.jsonl"): filepath.Join(dst, "subagents", "sa-3.jsonl"),
	} {
		b, err := os.ReadFile(from)
		if err != nil {
			t.Fatal(err)
		}
		os.MkdirAll(filepath.Dir(to), 0o700)
		if err := os.WriteFile(to, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHooklessCursorSessionFromTranscript(t *testing.T) {
	home := withHome(t)
	e := newEnv(t)
	copyCursorTranscripts(t, home, "cv-7")
	e.poll()

	s := e.session("cursor:cv-7")
	if s.Turns != 3 || s.ToolCalls != 11 || s.ToolErrors != 0 || s.LinesAdded != 7 || s.LinesRemoved != 2 || len(s.Files) != 6 {
		t.Fatalf("session: turns=%d tools=%d errs=%d +%d -%d files=%v", s.Turns, s.ToolCalls, s.ToolErrors, s.LinesAdded, s.LinesRemoved, s.Files)
	}
	if s.Status != "failed" || s.Usage != "none" || s.Title == "" || !s.StartedAt.Equal(time.Date(2026, 8, 6, 5, 11, 0, 1e6, time.UTC)) {
		t.Fatalf("session: usage=%s status=%s title=%q started=%v", s.Usage, s.Status, s.Title, s.StartedAt)
	}
	if s.PlanTotal != 2 || s.PlanDone != 0 {
		t.Fatalf("plan from the transcript: %d/%d", s.PlanDone, s.PlanTotal)
	}
	sub := e.session("cursor:cv-7/sub:sa-3")
	if sub.ParentID != "cursor:cv-7" || sub.ToolCalls != 1 || sub.Status != "done" {
		t.Fatalf("subagent: %+v", sub)
	}
	// Read again after a restart: nothing is counted twice.
	n := e.eventCount()
	e.restart()
	e.d.Backfill(time.Now().Add(-time.Hour))
	e.poll()
	if got := e.eventCount(); got != n {
		t.Fatalf("events after restart: %d, want %d", got, n)
	}
}

// A session with hooks also has a transcript; its activity comes from
// the hooks only.
func TestCursorHooksWinOverTranscript(t *testing.T) {
	home := withHome(t)
	e := newEnv(t)
	lines, err := readLines("../../pkg/adapters/cursor/testdata/2026.09/hooks.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range lines {
		shim.Run([]string{"--agent", "cursor"}, strings.NewReader(l))
	}
	copyCursorTranscripts(t, home, "cv-1")
	e.poll()
	e.poll()

	s := e.session("cursor:cv-1")
	if s.Turns != 1 || s.ToolCalls != 6 || s.LinesAdded != 2 || s.LinesRemoved != 1 {
		t.Fatalf("session: turns=%d tools=%d +%d -%d", s.Turns, s.ToolCalls, s.LinesAdded, s.LinesRemoved)
	}
	if s.PlanTotal != 2 || s.PlanDone != 2 || len(s.PlanItems) != 3 { // the transcript's list is the hooks' too
		t.Fatalf("plan: %d/%d %+v", s.PlanDone, s.PlanTotal, s.PlanItems)
	}
	if _, ok := e.d.transcripts[filepath.Join(home, ".cursor", "projects", "home-dev-shop", "agent-transcripts", "cv-1", "subagents", "sa-3.jsonl")]; !ok {
		t.Fatal("transcript not discovered")
	}
	if s, _ := e.st.Session(ctx, "cursor:cv-1/sub:sa-3"); s != nil {
		t.Fatal("transcript-only subagent activity recorded for a hooked session")
	}
}

func readLines(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n"), nil
}
