package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/elephaant/shiplino/pkg/engine"
	"github.com/elephaant/shiplino/pkg/insights"
	"github.com/elephaant/shiplino/pkg/model"
	"github.com/elephaant/shiplino/pkg/projects"
)

func TestBuildInsights(t *testing.T) {
	loc := time.FixedZone("test", 5*3600+1800)
	to := time.Date(2026, 10, 10, 0, 0, 0, 0, loc)
	from := to.AddDate(0, 0, -7)
	prev := from.AddDate(0, 0, -7)
	day := func(d int) time.Time { return from.AddDate(0, 0, d).Add(10 * time.Hour) }
	all := []*engine.Session{
		{ID: "cc:1", RootID: "cc:1", Agent: "claude-code", ProjectID: "p1", Model: "opus", StartedAt: day(0), BestCostUSD: 2, CostUSD: 1.5, CostSource: "reported",
			ActiveMS: 60_000, WaitingMS: 5000, Turns: 3, InputTokens: 100, OutputTokens: 10, Files: []string{"a"}, LinesAdded: 5,
			Links: []engine.Link{{Kind: "commit"}, {Kind: "pr", URL: "https://example.test/o/r/pull/1"}, {Kind: "pr", URL: "https://example.test/o/r/pull/2"}}},
		{ID: "cc:1/sub:x", RootID: "cc:1", ParentID: "cc:1", Agent: "claude-code", Model: "haiku", StartedAt: day(1), CostUSD: 0.5, InputTokens: 50},
		{ID: "cx:2", RootID: "cx:2", Agent: "codex", ProjectID: "p2", Model: "gpt", StartedAt: day(6), InputTokens: 1000, Usage: engine.UsageTokens},
		{ID: "cc:old", RootID: "cc:old", Agent: "claude-code", ProjectID: "p1", StartedAt: prev.Add(time.Hour), BestCostUSD: 7},
		{ID: "cc:older", RootID: "cc:older", Agent: "claude-code", StartedAt: prev.AddDate(0, 0, -1), BestCostUSD: 100},
	}
	in := buildInsights(all, from, to, prev, 7, nil, map[string]string{"p1": "api", "p2": "site"},
		map[string]bool{"https://example.test/o/r/pull/1": true})

	if in.Totals.Sessions != 2 || in.Totals.CostUSD != 2 || in.Totals.InputTokens != 1150 || in.Totals.Commits != 1 || in.Totals.PRs != 2 || in.Totals.PRsMerged != 1 || in.Totals.ActiveMS != 60_000 {
		t.Fatalf("totals: %+v", in.Totals)
	}
	if in.Previous.Sessions != 1 || in.Previous.CostUSD != 7 {
		t.Fatalf("previous: %+v", in.Previous)
	}
	if len(in.Daily) != 7 || in.Daily[0].CostUSD["claude-code"] != 2 || in.Daily[6].Sessions != 1 || in.Daily[1].Sessions != 0 {
		b, _ := json.Marshal(in.Daily)
		t.Fatalf("daily: %s", b)
	}
	if in.Agents[0].Key != "claude-code" || in.Agents[0].InputTokens != 150 || in.Agents[1].InputTokens != 1000 {
		t.Fatalf("agents: %+v", in.Agents)
	}
	if in.Projects[0].Name != "api" || in.Projects[1].Name != "site" {
		t.Fatalf("projects: %+v", in.Projects)
	}
	if len(in.Models) != 3 || in.Models[0].Key != "opus" || in.Models[0].CostUSD != 1.5 {
		t.Fatalf("models: %+v", in.Models)
	}
	if in.CostSources["reported"] != 1 || in.CostSources["unpriced"] != 1 || in.CostSources["none"] != 0 {
		t.Fatalf("sources: %v", in.CostSources)
	}
}

func TestInsightsEndpoint(t *testing.T) {
	f := setup(t)
	resp, body := f.get(t, "/api/v1/insights?days=7", bearer)
	var in Insights
	if resp.StatusCode != 200 || json.Unmarshal(body, &in) != nil || in.Totals.Sessions != 1 || len(in.Daily) != 7 {
		t.Fatalf("insights: %d %s", resp.StatusCode, body)
	}
	if resp, _ := f.get(t, "/api/v1/insights?days=0", bearer); resp.StatusCode != 400 {
		t.Fatalf("bad days: %d", resp.StatusCode)
	}
}

func TestInsightsFailures(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	tx, _ := f.s.st.Begin(ctx)
	sid := "claude-code:f1"
	proj := &model.Project{ID: "github.com/acme/api", CWD: "/home/dev/api"}
	tx.PutProject(ctx, projects.Project{ID: proj.ID, Name: "api", Kind: "git", RepoRoot: "/home/dev/api"}, now)
	type step struct {
		kind model.Kind
		data map[string]any
	}
	steps := []step{
		{model.KindToolStart, map[string]any{"tool_call_id": "t1", "tool": "shell", "tool_raw": "Bash", "input_summary": "go test ./auth -run TestSecretThing"}},
		{model.KindToolEnd, map[string]any{"tool_call_id": "t1", "tool": "shell", "ok": false, "error": "FAIL: TestSecretThing"}},
		{model.KindShellExec, map[string]any{"tool_call_id": "t1", "command": "go test ./auth -run TestSecretThing", "program": "go", "exit_code": 1}},
	}
	for i := range 3 {
		id := fmt.Sprint("e", i)
		steps = append(steps,
			step{model.KindToolStart, map[string]any{"tool_call_id": id, "tool": "edit", "tool_raw": "Edit", "input_summary": "/home/dev/api/src/auth.go"}},
			step{model.KindToolEnd, map[string]any{"tool_call_id": id, "tool": "edit", "ok": false, "error": "old_string not found"}})
	}
	steps = append(steps, step{model.KindTurnEnd, map[string]any{"status": "error", "error": "API overloaded"}})
	eng := engine.New(nil, nil)
	for i, ev := range steps {
		at := now.Add(time.Duration(i) * time.Second)
		e := model.Event{ID: model.NewULID(at), V: 1, TS: at, Kind: ev.kind, Agent: model.Agent{Name: "claude-code"},
			Collector: model.CollectorHook, SessionID: sid, ActorID: sid, DedupKey: fmt.Sprint("f", i), Data: ev.data, Project: proj}
		tx.InsertEvent(ctx, e)
		for _, x := range eng.Apply(e) {
			tx.PutSession(ctx, x)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	resp, body := f.get(t, "/api/v1/insights?days=7", bearer)
	var in struct {
		Failures json.RawMessage `json:"failures"`
	}
	if resp.StatusCode != 200 || json.Unmarshal(body, &in) != nil {
		t.Fatalf("insights: %d %s", resp.StatusCode, body)
	}
	var fl insights.Failures
	if err := json.Unmarshal(in.Failures, &fl); err != nil {
		t.Fatal(err)
	}
	if fl.ToolFailures != 4 || fl.ShellFailures != 1 || fl.EndedBadly != 1 || fl.RetryLoops != 1 || fl.Shell[0].Program != "go" || fl.Shell[0].IDs[0] != sid ||
		fl.Tools[0].ToolRaw != "Edit" || fl.Tools[1].ToolRaw != "Bash" || fl.Endings[0].Status != "error" || fl.Projects[0].Name != "api" ||
		fl.Loops[0].Target != "src/auth.go" {
		t.Fatalf("failures: %s", in.Failures)
	}
	// The report is metadata only: no command, error or argument text.
	for _, s := range []string{"TestSecretThing", "overloaded", "./auth", "old_string", "/home/dev"} {
		if strings.Contains(string(in.Failures), s) {
			t.Errorf("failures contain %q: %s", s, in.Failures)
		}
	}
}

func TestRelPath(t *testing.T) {
	cases := []struct{ root, in, want string }{
		{"/home/dev/api", "/home/dev/api/src/a.go", "src/a.go"},
		{"/home/dev/api", "/home/dev/other/b.go", "…/b.go"},
		{"", "/home/dev/api/a.go", "…/a.go"},
		{"/home/dev/api", "src/a.go", "src/a.go"},
		{"/home/dev/api", "/home/dev/api", "."},
	}
	for _, c := range cases {
		if got := relPath(c.root, c.in); got != c.want {
			t.Errorf("relPath(%q, %q) = %q, want %q", c.root, c.in, got, c.want)
		}
	}
}
