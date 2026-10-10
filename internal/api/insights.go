// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package api

import (
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/elephaant/shiplino/internal/store"
	"github.com/elephaant/shiplino/pkg/engine"
)

// Totals are the headline numbers for a period. Sessions are counted in
// the period they started; cost and time belong to those sessions.
type Totals struct {
	Sessions     int     `json:"sessions"`
	CostUSD      float64 `json:"cost_usd"`
	ActiveMS     int64   `json:"active_ms"`  // time agents spent working (in turns)
	WaitingMS    int64   `json:"waiting_ms"` // time agents spent waiting on you
	Turns        int     `json:"turns"`
	ToolCalls    int     `json:"tool_calls"`
	ToolErrors   int     `json:"tool_errors"`
	Files        int     `json:"files"`
	LinesAdded   int     `json:"lines_added"`
	LinesRemoved int     `json:"lines_removed"`
	Commits      int     `json:"commits"`
	PRs          int     `json:"prs"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CacheRead    int64   `json:"cache_read_tokens"`
	CacheWrite   int64   `json:"cache_write_tokens"`
}

// Day is one day of the series (in the daemon's local time zone).
type Day struct {
	Date     string             `json:"date"` // YYYY-MM-DD
	Sessions int                `json:"sessions"`
	CostUSD  map[string]float64 `json:"cost_usd"` // by agent
	ActiveMS int64              `json:"active_ms"`
}

// Breakdown is one row of a by-agent / by-project / by-model table.
type Breakdown struct {
	Key          string  `json:"key"`
	Name         string  `json:"name,omitempty"`
	Sessions     int     `json:"sessions"`
	CostUSD      float64 `json:"cost_usd"`
	ActiveMS     int64   `json:"active_ms"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CacheRead    int64   `json:"cache_read_tokens"`
	LinesAdded   int     `json:"lines_added"`
	LinesRemoved int     `json:"lines_removed"`
}

// Insights is the response of GET /api/v1/insights.
type Insights struct {
	From     time.Time         `json:"from"`
	To       time.Time         `json:"to"`
	Days     int               `json:"days"`
	Totals   Totals            `json:"totals"`
	Previous Totals            `json:"previous"` // the same length of time just before
	Daily    []Day             `json:"daily"`
	Agents   []Breakdown       `json:"agents"`
	Projects []Breakdown       `json:"projects"`
	Models   []Breakdown       `json:"models"` // tokens per model; cost computed per response
	Tools    []store.ToolCount `json:"tools"`
	// CostSources counts top-level sessions by where their cost comes from.
	// "unpriced": tokens without a price; "none": no usage recorded at all.
	CostSources map[string]int `json:"cost_sources"` // reported | computed | unpriced | none
}

// insights: GET /api/v1/insights?days=30&project=
func (s *Server) insights(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	days := 30
	if v := q.Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 366 {
			writeError(w, http.StatusBadRequest, "days must be 1-366")
			return
		}
		days = n
	}
	project := q.Get("project")
	now := time.Now()
	y, m, d := now.Date()
	to := time.Date(y, m, d+1, 0, 0, 0, 0, now.Location()) // end of today, local
	from := to.AddDate(0, 0, -days)
	prevFrom := from.AddDate(0, 0, -days)

	all, err := s.st.SessionsIn(r.Context(), project, 0)
	if err != nil {
		s.internal(w, err)
		return
	}
	tools, err := s.st.ToolCounts(r.Context(), from, to, project, 10)
	if err != nil {
		s.internal(w, err)
		return
	}
	names := map[string]string{}
	if ps, err := s.st.Projects(r.Context()); err == nil {
		for _, p := range ps {
			names[p.ID] = p.Name
		}
	}
	writeJSON(w, http.StatusOK, buildInsights(all, from, to, prevFrom, days, tools, names))
}

func buildInsights(all []*engine.Session, from, to, prevFrom time.Time, days int, tools []store.ToolCount, names map[string]string) Insights {
	out := Insights{From: from, To: to, Days: days, Tools: tools, CostSources: map[string]int{}}
	if out.Tools == nil {
		out.Tools = []store.ToolCount{}
	}
	byDay := map[string]*Day{}
	for i := 0; i < days; i++ {
		date := from.AddDate(0, 0, i).Format("2006-01-02")
		day := &Day{Date: date, CostUSD: map[string]float64{}}
		byDay[date] = day
		out.Daily = append(out.Daily, Day{})
	}
	agents, projects, models := map[string]*Breakdown{}, map[string]*Breakdown{}, map[string]*Breakdown{}
	get := func(m map[string]*Breakdown, k string) *Breakdown {
		if m[k] == nil {
			m[k] = &Breakdown{Key: k}
		}
		return m[k]
	}
	// Subagents' sessions belong to their root's period.
	rootStart := map[string]time.Time{}
	for _, x := range all {
		if x.ParentID == "" {
			rootStart[x.ID] = x.StartedAt
		}
	}
	for _, x := range all {
		start, ok := rootStart[x.RootID]
		if !ok {
			start = x.StartedAt
		}
		var t *Totals
		switch {
		case !start.Before(from) && start.Before(to):
			t = &out.Totals
		case !start.Before(prevFrom) && start.Before(from):
			t = &out.Previous
		default:
			continue
		}
		// Every actor's own tokens and work count; cost and session counts
		// come from top-level sessions (their cost already includes their
		// subagents').
		t.InputTokens += x.InputTokens
		t.OutputTokens += x.OutputTokens
		t.CacheRead += x.CacheReadTokens
		t.CacheWrite += x.CacheWriteTokens
		t.ToolCalls += x.ToolCalls
		t.ToolErrors += x.ToolErrors
		t.Files += len(x.Files)
		t.LinesAdded += x.LinesAdded
		t.LinesRemoved += x.LinesRemoved
		root := x.ParentID == ""
		if root {
			t.Sessions++
			t.CostUSD += x.BestCostUSD
			t.ActiveMS += x.ActiveMS
			t.WaitingMS += x.WaitingMS
			t.Turns += x.Turns
			for _, l := range x.Links {
				switch l.Kind {
				case "commit":
					t.Commits++
				case "pr":
					t.PRs++
				}
			}
		}
		if t != &out.Totals {
			continue
		}
		if x.Model != "" {
			m := get(models, x.Model)
			m.InputTokens += x.InputTokens
			m.OutputTokens += x.OutputTokens
			m.CacheRead += x.CacheReadTokens
			m.CostUSD += x.CostUSD
			if root {
				m.Sessions++
			}
		}
		if !root {
			continue
		}
		src := x.CostSource
		if src == "" {
			src = engine.UsageNone
			if x.Usage == engine.UsageTokens {
				src = "unpriced"
			}
		}
		out.CostSources[src]++
		for _, b := range []*Breakdown{get(agents, x.Agent), get(projects, x.ProjectID)} {
			b.Sessions++
			b.CostUSD += x.BestCostUSD
			b.ActiveMS += x.ActiveMS
			b.LinesAdded += x.LinesAdded
			b.LinesRemoved += x.LinesRemoved
		}
		if day := byDay[start.In(from.Location()).Format("2006-01-02")]; day != nil {
			day.Sessions++
			day.CostUSD[x.Agent] += x.BestCostUSD
			day.ActiveMS += x.ActiveMS
		}
	}
	// Agent token totals include their subagents.
	for _, x := range all {
		if start, ok := rootStart[x.RootID]; ok && !start.Before(from) && start.Before(to) {
			b := get(agents, x.Agent)
			b.InputTokens += x.InputTokens
			b.OutputTokens += x.OutputTokens
			b.CacheRead += x.CacheReadTokens
		}
	}
	for i := range out.Daily {
		out.Daily[i] = *byDay[from.AddDate(0, 0, i).Format("2006-01-02")]
	}
	out.Agents, out.Projects, out.Models = sorted(agents, nil), sorted(projects, names), sorted(models, nil)
	return out
}

func sorted(m map[string]*Breakdown, names map[string]string) []Breakdown {
	out := make([]Breakdown, 0, len(m))
	for _, b := range m {
		if names != nil {
			b.Name = names[b.Key]
			if b.Key == "" {
				b.Name = "Unsorted"
			}
		}
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CostUSD != out[j].CostUSD {
			return out[i].CostUSD > out[j].CostUSD
		}
		if out[i].Sessions != out[j].Sessions {
			return out[i].Sessions > out[j].Sessions
		}
		return out[i].Key < out[j].Key
	})
	return out
}
