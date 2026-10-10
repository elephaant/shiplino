package bench

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/elephaant/shiplino/pkg/engine"
	"github.com/elephaant/shiplino/pkg/insights"
	"github.com/elephaant/shiplino/pkg/model"
	"github.com/elephaant/shiplino/pkg/projects"
)

// targetFailureReport is the p95 of building the failure report from the
// stored sessions: no query of its own, the engine folded the failures.
const targetFailureReport = 30 * time.Millisecond

// TestInsightsWithHistory times the insights endpoint and its failure
// report over a long history: root sessions spread over 90 days, 40 tool
// calls each, every third session with failed commands and edits (folded
// by the engine, as the daemon does).
func TestInsightsWithHistory(t *testing.T) {
	roots := 200
	if *full {
		roots = 2000
	}
	r := newRig(t, nil)
	seedCalls(t, r, roots, 40)
	var ins, report []time.Duration
	for range 10 {
		t0 := time.Now()
		r.get(t, "/api/v1/insights?days=30")
		ins = append(ins, time.Since(t0))
	}
	all, err := r.st.SessionsIn(context.Background(), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for range 10 {
		t0 := time.Now()
		entries := make([]insights.Entry, 0, len(all))
		for _, s := range all {
			entries = append(entries, insights.Entry{Root: s.RootID, Agent: s.Agent, Project: s.ProjectID, IsRoot: s.ParentID == "", At: s.LastEventAt, Tally: s.Failures})
		}
		f := insights.Report(entries)
		report = append(report, time.Since(t0))
		if f.RetryLoops == 0 || f.ShellFailures == 0 {
			t.Fatalf("report: %+v", f)
		}
	}
	t.Logf("insights endpoint, 30 days of %d root sessions × 40 tool calls: %s", roots, summary(ins))
	t.Logf("failure report over all %d sessions:                          %s", len(all), summary(report))
	if *full {
		if p := pct(report, 95); p > targetFailureReport {
			t.Errorf("failure report p95 = %s, target < %s", p, targetFailureReport)
		}
	}
}

// seedCalls stores `roots` sessions, one every ~65 minutes going back
// (90 days for 2,000), each with `calls` shell and edit calls.
func seedCalls(t testing.TB, r *rig, roots, calls int) {
	t.Helper()
	ctx := context.Background()
	p := projects.Project{ID: "github.com/acme/history", Name: "history", Kind: "git", RepoRoot: "/home/dev/history"}
	now := time.Now().UTC()
	tx, err := r.st.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	eng := engine.New(nil, nil)
	put := func(sid string, kind model.Kind, at time.Time, data map[string]any) {
		n++
		e := model.Event{ID: model.NewULID(at), V: 1, TS: at, Kind: kind, Agent: model.Agent{Name: "claude-code"}, Collector: model.CollectorHook,
			SessionID: sid, ActorID: sid, DedupKey: fmt.Sprint("seed-", n), Data: data, Project: &model.Project{ID: p.ID}}
		if _, err := tx.InsertEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
		eng.Apply(e)
	}
	for i := range roots {
		start := now.Add(-time.Duration(i) * 65 * time.Minute)
		sid := fmt.Sprintf("claude-code:i-%05d", i)
		put(sid, model.KindSessionStart, start, map[string]any{})
		for c := range calls {
			at := start.Add(time.Duration(c) * time.Second)
			id := fmt.Sprint("tu-", c)
			fail := i%3 == 0 && c%20 < 6 // three of each in a row: retry loops
			if c%2 == 0 {
				put(sid, model.KindToolStart, at, map[string]any{"tool_call_id": id, "tool": "shell", "tool_raw": "Bash", "input_summary": "go test ./..."})
				put(sid, model.KindToolEnd, at, map[string]any{"tool_call_id": id, "tool": "shell", "ok": !fail})
				exit := 0
				if fail {
					exit = 1
				}
				put(sid, model.KindShellExec, at, map[string]any{"tool_call_id": id, "command": "go test ./...", "program": "go", "exit_code": exit})
			} else {
				put(sid, model.KindToolStart, at, map[string]any{"tool_call_id": id, "tool": "edit", "tool_raw": "Edit", "input_summary": "/home/dev/history/a.go"})
				put(sid, model.KindToolEnd, at, map[string]any{"tool_call_id": id, "tool": "edit", "ok": !fail})
			}
		}
		put(sid, model.KindTurnEnd, start.Add(time.Duration(calls)*time.Second), map[string]any{"status": "ok"})
		if err := tx.PutSession(ctx, eng.Get(sid)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.PutProject(ctx, p, now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
