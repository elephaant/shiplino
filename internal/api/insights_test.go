// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

package api

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/elephaant/shiplino/pkg/engine"
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
			Links: []engine.Link{{Kind: "commit"}, {Kind: "pr"}}},
		{ID: "cc:1/sub:x", RootID: "cc:1", ParentID: "cc:1", Agent: "claude-code", Model: "haiku", StartedAt: day(1), CostUSD: 0.5, InputTokens: 50},
		{ID: "cx:2", RootID: "cx:2", Agent: "codex", ProjectID: "p2", Model: "gpt", StartedAt: day(6), InputTokens: 1000, Usage: engine.UsageTokens},
		{ID: "cc:old", RootID: "cc:old", Agent: "claude-code", ProjectID: "p1", StartedAt: prev.Add(time.Hour), BestCostUSD: 7},
		{ID: "cc:older", RootID: "cc:older", Agent: "claude-code", StartedAt: prev.AddDate(0, 0, -1), BestCostUSD: 100},
	}
	in := buildInsights(all, from, to, prev, 7, nil, map[string]string{"p1": "api", "p2": "site"})

	if in.Totals.Sessions != 2 || in.Totals.CostUSD != 2 || in.Totals.InputTokens != 1150 || in.Totals.Commits != 1 || in.Totals.PRs != 1 || in.Totals.ActiveMS != 60_000 {
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
