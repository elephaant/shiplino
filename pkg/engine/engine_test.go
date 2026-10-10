package engine

import (
	"testing"
	"time"

	"github.com/elephaant/shiplino/pkg/model"
)

var t0 = time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)

const sid = "claude-code:s1"

func ev(sec int, kind model.Kind, data map[string]any) model.Event {
	return model.Event{
		TS: t0.Add(time.Duration(sec) * time.Second), Kind: kind, Agent: model.Agent{Name: "claude-code"},
		SessionID: sid, ActorID: sid, Project: &model.Project{CWD: "/work/demo"}, Data: data,
	}
}

func TestSessionLifecycle(t *testing.T) {
	e := New(nil, nil)
	steps := []struct {
		ev         model.Event
		wantStatus Status
		wantDoing  string
	}{
		{ev(0, model.KindSessionStart, map[string]any{"model": "claude-sonnet-5-5"}), StatusRunning, ""},
		{ev(1, model.KindTurnStart, map[string]any{"prompt": "Fix the login bug. Then add a test.\nMore detail"}), StatusRunning, "Thinking…"},
		{ev(2, model.KindToolStart, map[string]any{"tool": "edit", "tool_raw": "Edit", "input_summary": "/work/demo/src/auth.ts"}), StatusRunning, "Editing auth.ts"},
		{ev(3, model.KindToolEnd, map[string]any{"ok": true}), StatusRunning, "Editing auth.ts"},
		{ev(3, model.KindFileEdit, map[string]any{"path": "/work/demo/src/auth.ts", "lines_added": 12.0, "lines_removed": 3.0}), StatusRunning, "Editing auth.ts"},
		{ev(4, model.KindWaitingStart, map[string]any{"message": "Approve: npm test"}), StatusWaiting, "Approve: npm test"},
		{ev(10, model.KindToolStart, map[string]any{"tool": "shell", "input_summary": "npm test"}), StatusRunning, "Running npm test"},
		{ev(12, model.KindToolEnd, map[string]any{"ok": false}), StatusRunning, "Running npm test"},
		{ev(13, model.KindTurnEnd, map[string]any{"status": "ok"}), StatusReview, ""},
		{ev(20, model.KindSessionEnd, map[string]any{"reason": "exit"}), StatusReview, ""},
	}
	for i, st := range steps {
		e.Apply(st.ev)
		s := e.Get(sid)
		if s.Status != st.wantStatus || s.NowDoing != st.wantDoing {
			t.Fatalf("step %d (%s): status=%s doing=%q, want %s %q", i, st.ev.Kind, s.Status, s.NowDoing, st.wantStatus, st.wantDoing)
		}
	}
	s := e.Get(sid)
	if s.Title != "Fix the login bug." {
		t.Errorf("title = %q", s.Title)
	}
	if s.Model != "claude-sonnet-5-5" || s.Turns != 1 || s.ToolCalls != 2 || s.ToolErrors != 1 {
		t.Errorf("counters: %+v", s)
	}
	if s.FilesChanged() != 1 || s.LinesAdded != 12 || s.LinesRemoved != 3 {
		t.Errorf("files: %v +%d -%d", s.Files, s.LinesAdded, s.LinesRemoved)
	}
	if s.WaitingMS != 6000 {
		t.Errorf("waiting_ms = %d, want 6000", s.WaitingMS)
	}
	if !s.EndedAt.Equal(t0.Add(20 * time.Second)) {
		t.Errorf("ended_at = %v", s.EndedAt)
	}
}

func TestTurnEndWithoutEditsIsDone(t *testing.T) {
	e := New(nil, nil)
	e.Apply(ev(0, model.KindTurnStart, map[string]any{"prompt": "what does this repo do?"}))
	e.Apply(ev(1, model.KindTurnEnd, map[string]any{"status": "ok"}))
	if s := e.Get(sid); s.Status != StatusDone {
		t.Fatalf("status = %s", s.Status)
	}
}

func TestTurnEndErrorFails(t *testing.T) {
	e := New(nil, nil)
	e.Apply(ev(0, model.KindTurnStart, nil))
	e.Apply(ev(1, model.KindTurnEnd, map[string]any{"status": "error", "error": "rate_limit"}))
	if s := e.Get(sid); s.Status != StatusFailed || s.NowDoing != "rate_limit" {
		t.Fatalf("status = %s doing = %q", s.Status, s.NowDoing)
	}
	// A later tool end must not resurrect a failed session.
	e.Apply(ev(2, model.KindToolEnd, map[string]any{"ok": true}))
	if s := e.Get(sid); s.Status != StatusFailed {
		t.Fatalf("status after tool.end = %s", s.Status)
	}
}

func TestSubagents(t *testing.T) {
	e := New(nil, nil)
	child := sid + "/sub:a1"
	changed := e.Apply(ev(0, model.KindSubagentStart, map[string]any{"child_session_id": child, "agent_type": "reviewer"}))
	if len(changed) != 2 {
		t.Fatalf("changed = %d, want parent + child", len(changed))
	}
	sub := ev(1, model.KindToolStart, map[string]any{"tool": "read", "input_summary": "/work/demo/a.go"})
	sub.ActorID, sub.ParentActor, sub.ActorType = child, sid, "reviewer"
	e.Apply(sub)

	c := e.Get(child)
	if c.ParentID != sid || c.RootID != sid || c.Depth != 1 || c.ActorType != "reviewer" {
		t.Fatalf("child: %+v", c)
	}
	if c.NowDoing != "Reading a.go" || c.ToolCalls != 1 {
		t.Fatalf("child activity: %+v", c)
	}
	if p := e.Get(sid); p.ToolCalls != 0 {
		t.Fatalf("parent counted the child's tool call: %d", p.ToolCalls)
	}
	e.Apply(ev(5, model.KindSubagentEnd, map[string]any{"child_session_id": child, "status": "done"}))
	if c := e.Get(child); c.Status != StatusDone || c.EndedAt.IsZero() {
		t.Fatalf("child after end: %+v", c)
	}
}

func TestUsageAccumulates(t *testing.T) {
	e := New(nil, nil)
	e.Apply(ev(0, model.KindUsage, map[string]any{"model": "m", "input_tokens": 100.0, "output_tokens": 20.0, "cache_read_tokens": 1000.0, "cost_usd": 0.01}))
	e.Apply(ev(1, model.KindUsage, map[string]any{"input_tokens": int64(50), "output_tokens": 5, "cost_usd": 0.005}))
	s := e.Get(sid)
	if s.InputTokens != 150 || s.OutputTokens != 25 || s.CacheReadTokens != 1000 || s.CostUSD < 0.0149 || s.CostUSD > 0.0151 {
		t.Fatalf("usage: %+v", s)
	}
}

func TestPrimedEngineContinues(t *testing.T) {
	prev := &Session{ID: sid, Agent: "claude-code", RootID: sid, Status: StatusWaiting, WaitingSince: t0, Turns: 3}
	e := New([]*Session{prev}, nil)
	e.Apply(ev(4, model.KindToolEnd, map[string]any{"ok": true}))
	if s := e.Get(sid); s.Status != StatusRunning || s.WaitingMS != 4000 || s.Turns != 3 {
		t.Fatalf("resumed: %+v", s)
	}
}

func TestTitleFromPrompt(t *testing.T) {
	cases := map[string]string{
		"  fix   the bug  ":                "fix the bug",
		"Why is CI red? Check logs.":       "Why is CI red?",
		"line one\nline two":               "line one",
		string(make([]rune, 0)):            "",
		"a very long prompt " + repeat(80): "a very long prompt " + repeat(80)[:60] + "…",
	}
	for in, want := range cases {
		if got := TitleFromPrompt(in); got != want {
			t.Errorf("TitleFromPrompt(%q) = %q, want %q", in, got, want)
		}
	}
}

func repeat(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'x'
	}
	return string(b)
}

func TestCostRollupAndReports(t *testing.T) {
	e := New(nil, nil)
	child := sid + "/sub:a1"
	e.Apply(ev(0, model.KindSubagentStart, map[string]any{"child_session_id": child}))
	e.Apply(ev(1, model.KindUsage, map[string]any{"cost_usd": 1.0}))
	sub := ev(2, model.KindUsage, map[string]any{"cost_usd": 0.5})
	sub.ActorID, sub.ParentActor = child, sid
	changed := e.Apply(sub)
	if len(changed) != 2 {
		t.Fatalf("subagent cost should also change the parent, got %d", len(changed))
	}
	root := e.Get(sid)
	if root.CostUSD != 1.0 || root.TreeCostUSD != 1.5 || root.BestCostUSD != 1.5 || root.CostSource != "computed" {
		t.Fatalf("root: own=%v tree=%v best=%v (%s)", root.CostUSD, root.TreeCostUSD, root.BestCostUSD, root.CostSource)
	}

	report := func(sec int, session, proc string, total float64) {
		r := ev(sec, model.KindUsage, map[string]any{"report": true, "process": proc, "total_cost_usd": total})
		r.SessionID, r.ActorID = session, session
		e.Apply(r)
	}
	report(3, sid, "p1", 1.8) // the agent saw more than the transcript: 1.8 > 1.5
	if root.ReportedCostUSD != 1.8 || root.BestCostUSD != 1.8 || root.CostSource != "reported" {
		t.Fatalf("after report: %+v", root)
	}
	report(4, sid, "p1", 1.8) // a repeated total adds nothing
	// The same process moves on to another session (e.g. after /clear):
	// only the increase belongs to it.
	report(5, "claude-code:s2", "p1", 2.5)
	if s2 := e.Get("claude-code:s2"); s2.ReportedCostUSD < 0.699 || s2.ReportedCostUSD > 0.701 {
		t.Fatalf("second session got %v, want 0.7", s2.ReportedCostUSD)
	}
	if root.ReportedCostUSD != 1.8 {
		t.Fatalf("first session changed: %v", root.ReportedCostUSD)
	}
	// Totals survive a restart through ProcessTotals.
	e2 := New(nil, e.ProcessTotals())
	r := ev(6, model.KindUsage, map[string]any{"report": true, "process": "p1", "total_cost_usd": 2.5})
	e2.Apply(r)
	if got := e2.Get(sid).ReportedCostUSD; got != 0 {
		t.Fatalf("replayed total re-counted after restart: %v", got)
	}
}

func TestAgentTitleWins(t *testing.T) {
	e := New(nil, nil)
	e.Apply(ev(0, model.KindTurnStart, map[string]any{"prompt": "please fix the flaky login test"}))
	if s := e.Get(sid); s.Title != "please fix the flaky login test" || s.TitleSource != "prompt" {
		t.Fatalf("prompt title: %q %q", s.Title, s.TitleSource)
	}
	e.Apply(ev(1, model.KindSessionUpdate, map[string]any{"title": "Fix flaky login test", "title_source": "agent"}))
	e.Apply(ev(2, model.KindTurnStart, map[string]any{"prompt": "now also update the docs"}))
	if s := e.Get(sid); s.Title != "Fix flaky login test" || s.TitleSource != "agent" {
		t.Fatalf("agent title should stick: %q %q", s.Title, s.TitleSource)
	}
	e.Apply(ev(3, model.KindSessionUpdate, map[string]any{"title": "Fix login test and docs"}))
	if s := e.Get(sid); s.Title != "Fix login test and docs" {
		t.Fatalf("newer agent title should replace: %q", s.Title)
	}
}

func TestLinks(t *testing.T) {
	e := New(nil, nil)
	pr := map[string]any{"url": "https://github.com/acme/api/pull/7", "number": 7, "action": "created"}
	e.Apply(ev(0, model.KindGitPR, pr))
	e.Apply(ev(1, model.KindGitPR, pr))
	e.Apply(ev(2, model.KindGitPush, map[string]any{"branch": "fix/login"}))
	s := e.Get(sid)
	if len(s.Links) != 2 || s.Links[0].Number != 7 || s.Links[1].Ref != "fix/login" {
		t.Fatalf("links: %+v", s.Links)
	}
}

func TestLateEventDoesNotReopenEndedSession(t *testing.T) {
	e := New(nil, nil)
	at := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	ev := func(kind model.Kind, ts time.Time, data map[string]any) model.Event {
		return model.Event{Kind: kind, TS: ts, SessionID: "a:1", ActorID: "a:1", Agent: model.Agent{Name: "a"}, Data: data}
	}
	e.Apply(ev(model.KindSessionStart, at, nil))
	e.Apply(ev(model.KindSessionEnd, at.Add(time.Minute), map[string]any{"status": "ended"}))
	e.Apply(ev(model.KindToolStart, at.Add(30*time.Second), map[string]any{"tool": "read"}))
	if s := e.Get("a:1"); s.Status != StatusDone || s.ToolCalls != 1 {
		t.Fatalf("status=%s tools=%d", s.Status, s.ToolCalls)
	}
	e.Apply(ev(model.KindTurnStart, at.Add(2*time.Minute), nil)) // resumed later
	if s := e.Get("a:1"); s.Status != StatusRunning {
		t.Fatalf("resume: %s", s.Status)
	}
}

func TestActiveTime(t *testing.T) {
	e := New(nil, nil)
	at := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	ev := func(kind model.Kind, ts time.Time, data map[string]any) model.Event {
		return model.Event{Kind: kind, TS: ts, SessionID: "a:1", ActorID: "a:1", Agent: model.Agent{Name: "a"}, Data: data}
	}
	e.Apply(ev(model.KindTurnStart, at, nil))
	e.Apply(ev(model.KindTurnEnd, at.Add(40*time.Second), map[string]any{"status": "ok"}))
	e.Apply(ev(model.KindTurnStart, at.Add(time.Hour), nil)) // an idle hour isn't counted
	e.Apply(ev(model.KindTurnEnd, at.Add(time.Hour+time.Minute), map[string]any{"status": "ok", "duration_ms": 50_000}))
	if s := e.Get("a:1"); s.ActiveMS != 90_000 {
		t.Fatalf("active = %d, want 90000 (40s measured + 50s the agent reported)", s.ActiveMS)
	}
}

func TestMarkIdle(t *testing.T) {
	e := New(nil, nil)
	at := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	ev := func(id string, kind model.Kind, ts time.Time) model.Event {
		return model.Event{Kind: kind, TS: ts, SessionID: id, ActorID: id, Agent: model.Agent{Name: "a"}, Data: map[string]any{"tool": "shell"}}
	}
	e.Apply(ev("quiet", model.KindTurnStart, at))
	e.Apply(ev("building", model.KindTurnStart, at))
	e.Apply(ev("building", model.KindToolStart, at)) // a long build is in progress
	e.Apply(ev("asking", model.KindWaitingStart, at))

	if got := e.MarkIdle(at.Add(29 * time.Minute)); len(got) != 0 {
		t.Fatalf("idle too early: %v", got)
	}
	got := e.MarkIdle(at.Add(31 * time.Minute))
	if len(got) != 1 || got[0].ID != "quiet" || got[0].Status != StatusIdle {
		t.Fatalf("31m: %+v", got)
	}
	if e.Get("asking").Status != StatusWaiting {
		t.Fatal("a waiting session must stay waiting")
	}
	if got := e.MarkIdle(at.Add(2*time.Hour + time.Minute)); len(got) != 1 || got[0].ID != "building" {
		t.Fatalf("in-flight tool: %+v", got)
	}
	e.Apply(ev("quiet", model.KindToolStart, at.Add(3*time.Hour)))
	if s := e.Get("quiet"); s.Status != StatusRunning {
		t.Fatalf("an event brings it back: %s", s.Status)
	}
}

// A session whose first event has no folder takes one from a later event.
func TestCWDFromLaterEvent(t *testing.T) {
	e := New(nil, nil)
	first := ev(0, model.KindTurnStart, map[string]any{"prompt": "hi"})
	first.Project = nil
	e.Apply(first)
	edit := ev(1, model.KindFileEdit, map[string]any{"path": "/work/demo/src/a.go"})
	edit.Project = &model.Project{CWD: "/work/demo/src"}
	e.Apply(edit)
	e.Apply(ev(2, model.KindShellExec, map[string]any{"command": "ls"}))
	if s := e.Get(sid); s.CWD != "/work/demo/src" {
		t.Fatalf("cwd = %q", s.CWD)
	}
}

func TestMergedPRMovesReviewToDone(t *testing.T) {
	e := New(nil, nil)
	at := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	ev := func(kind model.Kind, data map[string]any) model.Event {
		return model.Event{Kind: kind, TS: at, SessionID: "a:1", ActorID: "a:1", Agent: model.Agent{Name: "a"}, Data: data}
	}
	e.Apply(ev(model.KindTurnStart, nil))
	e.Apply(ev(model.KindFileEdit, map[string]any{"path": "x.go", "lines_added": 1}))
	e.Apply(ev(model.KindTurnEnd, map[string]any{"status": "ok"}))
	e.Apply(ev(model.KindGitPR, map[string]any{"url": "https://github.com/a/b/pull/1", "number": 1, "action": "linked"}))
	if s := e.Get("a:1"); s.Status != StatusReview || len(s.Links) != 1 {
		t.Fatalf("linked: %s %v", s.Status, s.Links)
	}
	e.Apply(ev(model.KindGitPR, map[string]any{"url": "https://github.com/a/b/pull/1", "number": 1, "action": "merged"}))
	if s := e.Get("a:1"); s.Status != StatusDone || len(s.Links) != 1 || s.Links[0].Action != "merged" {
		t.Fatalf("merged: %s %v", s.Status, s.Links)
	}
}
