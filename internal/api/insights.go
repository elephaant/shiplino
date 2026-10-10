package api

import (
	"cmp"
	"net/http"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/elephaant/shiplino/internal/store"
	"github.com/elephaant/shiplino/pkg/engine"
	"github.com/elephaant/shiplino/pkg/insights"
	"github.com/elephaant/shiplino/pkg/projects"
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
	PRsMerged    int     `json:"prs_merged"` // PRs whose state is merged (needs the GitHub integration)
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
	// Failures are the period's failed tool calls and commands, refused
	// permissions, retry loops and sessions that ended badly.
	Failures insights.Failures `json:"failures"`
	// Authorship splits the period's committed lines by who wrote them.
	Authorship Authorship `json:"authorship"`
}

// LineSplit is committed lines by author: the agents' edits, elsewhere,
// or unknown (no diffs, capture level below full).
type LineSplit struct {
	Key     string `json:"key"`
	Name    string `json:"name,omitempty"`
	Commits int    `json:"commits"`
	Agent   int    `json:"agent_lines"`
	Human   int    `json:"human_lines"`
	Unknown int    `json:"unknown_lines"`
}

// Authorship is committed-line authorship for a period: commits are
// counted on the day they were made, each once.
type Authorship struct {
	Totals   LineSplit   `json:"totals"`
	Daily    []LineSplit `json:"daily"` // key: YYYY-MM-DD
	Agents   []LineSplit `json:"agents"`
	Projects []LineSplit `json:"projects"`
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
	names, roots := map[string]string{}, map[string]string{}
	if ps, err := s.st.Projects(r.Context()); err == nil {
		for _, p := range ps {
			names[p.ID], roots[p.ID] = p.Name, projectRoot(p.Project)
		}
	}
	merged := map[string]bool{}
	if prs, err := s.st.PRs(r.Context()); err == nil {
		for u, pr := range prs {
			merged[u] = pr.State == "merged"
		}
	}
	out := buildInsights(all, from, to, prevFrom, days, tools, names, merged)
	commits, err := s.st.CommitLines(r.Context(), from, to, project)
	if err != nil {
		s.internal(w, err)
		return
	}
	out.Authorship = buildAuthorship(commits, from, days, names)
	// Loop targets are shown (and synced) relative to the project.
	for i, l := range out.Failures.Loops {
		out.Failures.Loops[i].Target = relPath(roots[l.Project], l.Target)
	}
	writeJSON(w, http.StatusOK, out)
}

func buildInsights(all []*engine.Session, from, to, prevFrom time.Time, days int, tools []store.ToolCount, names map[string]string, merged map[string]bool) Insights {
	out := Insights{From: from, To: to, Days: days, Tools: tools, CostSources: map[string]int{}}
	if out.Tools == nil {
		out.Tools = []store.ToolCount{}
	}
	var entries []insights.Entry
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
					if merged[l.URL] {
						t.PRsMerged++
					}
				}
			}
		}
		if t != &out.Totals {
			continue
		}
		entries = append(entries, insights.Entry{Root: x.RootID, Agent: x.Agent, Project: x.ProjectID, IsRoot: x.ParentID == "",
			At: x.LastEventAt, Tally: x.Failures})
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
	out.Failures = insights.Report(entries)
	for i, p := range out.Failures.Projects {
		out.Failures.Projects[i].Name = names[p.Key]
		if p.Key == "" {
			out.Failures.Projects[i].Name = "Unsorted"
		}
	}
	return out
}

func buildAuthorship(commits []store.CommitLines, from time.Time, days int, names map[string]string) Authorship {
	out := Authorship{Daily: make([]LineSplit, days)}
	index := map[string]int{}
	for i := range out.Daily {
		out.Daily[i].Key = from.AddDate(0, 0, i).Format("2006-01-02")
		index[out.Daily[i].Key] = i
	}
	agents, projects := map[string]*LineSplit{}, map[string]*LineSplit{}
	get := func(m map[string]*LineSplit, k string) *LineSplit {
		if m[k] == nil {
			m[k] = &LineSplit{Key: k}
		}
		return m[k]
	}
	for _, c := range commits {
		rows := []*LineSplit{&out.Totals, get(agents, c.Agent), get(projects, c.ProjectID)}
		if i, ok := index[c.TS.In(from.Location()).Format("2006-01-02")]; ok {
			rows = append(rows, &out.Daily[i])
		}
		for _, r := range rows {
			r.Commits++
			r.Agent += c.AgentLines
			r.Human += c.HumanLines
			r.Unknown += c.UnknownLines
		}
	}
	list := func(m map[string]*LineSplit, names map[string]string) []LineSplit {
		out := make([]LineSplit, 0, len(m))
		for _, r := range m {
			if names != nil {
				r.Name = cmp.Or(names[r.Key], r.Key, "Unsorted")
			}
			out = append(out, *r)
		}
		sort.Slice(out, func(i, j int) bool {
			a, b := out[i].Agent+out[i].Human+out[i].Unknown, out[j].Agent+out[j].Human+out[j].Unknown
			if a != b {
				return a > b
			}
			return out[i].Key < out[j].Key
		})
		return out
	}
	out.Agents, out.Projects = list(agents, nil), list(projects, names)
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

// projectRoot is a project's folder: its repository root, or the folder in
// a local project's id.
func projectRoot(p projects.Project) string {
	if p.RepoRoot != "" {
		return p.RepoRoot
	}
	for _, prefix := range []string{"local:", "dir:"} {
		if rest, ok := strings.CutPrefix(p.ID, prefix); ok {
			return rest
		}
	}
	return ""
}

// relPath makes a path relative to the project folder with the same rule
// as sync: a path outside it becomes "…/" and its base name. Unix and
// Windows paths are accepted on every OS.
func relPath(root, p string) string {
	if !isAbs(p) {
		return p
	}
	sp := filepath.ToSlash(p)
	if r := strings.TrimRight(filepath.ToSlash(root), "/"); r != "" && isAbs(root) {
		if sp == r {
			return "."
		}
		if rest, ok := strings.CutPrefix(sp, r+"/"); ok {
			return rest
		}
	}
	return "…/" + path.Base(strings.ReplaceAll(sp, `\`, "/"))
}

func isAbs(p string) bool {
	return filepath.IsAbs(p) || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) ||
		len(p) > 2 && p[1] == ':' && (p[2] == '\\' || p[2] == '/')
}
