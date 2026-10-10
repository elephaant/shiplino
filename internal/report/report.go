package report

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/elephaant/shiplino/pkg/engine"
	"github.com/elephaant/shiplino/pkg/insights"
)

// SchemaVersion is the version of the JSON output. Fields are only ever
// added within a version.
const SchemaVersion = 1

// Report is the output of `shiplino report --json`. It holds metadata
// only: names, counts, timings, tokens and costs, never prompts, replies
// or commands. Documented in docs/report.md.
type Report struct {
	Schema  int       `json:"schema"`
	Since   time.Time `json:"since"`
	Until   time.Time `json:"until"`
	Agent   string    `json:"agent,omitempty"`   // the --agent filter
	Project string    `json:"project,omitempty"` // the --project filter

	Transcripts int   `json:"transcripts"` // files read
	Unreadable  int   `json:"unreadable"`  // files that failed to read
	Lines       int64 `json:"lines"`
	BadLines    int64 `json:"bad_lines"` // lines that failed to parse or were too long

	Totals   Usage         `json:"totals"`
	Agents   []AgentUsage  `json:"agents"`
	Projects []ProjectRow  `json:"projects"`
	Models   []ModelRow    `json:"models"`
	Limits   []Limit       `json:"limits"`
	Failures FailureReport `json:"failures"`
}

// Usage sums sessions. Sessions, active time and cost count top-level
// sessions (their cost includes their subagents'); tokens count every
// actor's own responses.
type Usage struct {
	Sessions         int   `json:"sessions"`
	Subagents        int   `json:"subagents"`
	Turns            int   `json:"turns"`
	ToolCalls        int   `json:"tool_calls"`
	ActiveMS         int64 `json:"active_ms"` // time in turns, not idle time between them
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
	// CostUSD is the API-equivalent cost: what the tokens would cost at
	// list API prices. ReportedCostUSD is the part the agent priced itself,
	// ComputedCostUSD the part Shiplino priced from tokens.
	CostUSD         float64 `json:"cost_usd"`
	ReportedCostUSD float64 `json:"reported_cost_usd"`
	ComputedCostUSD float64 `json:"computed_cost_usd"`
	// Unpriced sessions have tokens with no known price; NoUsage sessions
	// did work but the agent records no token usage (e.g. Cursor).
	Unpriced int `json:"unpriced_sessions"`
	NoUsage  int `json:"no_usage_sessions"`
}

// AgentUsage is one agent's usage.
type AgentUsage struct {
	Agent string `json:"agent"`
	Usage
	Failures FailureCounts `json:"failures"`
}

// ProjectRow is one project's sessions.
type ProjectRow struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Sessions int     `json:"sessions"`
	ActiveMS int64   `json:"active_ms"`
	CostUSD  float64 `json:"cost_usd"`
}

// ModelRow is one model's tokens across every actor that used it as its
// main model, and the cost computed from them.
type ModelRow struct {
	Model            string  `json:"model"`
	Actors           int     `json:"actors"` // sessions and subagents
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	CostUSD          float64 `json:"cost_usd"`
}

// Limit is the newest state of one plan usage window the agent reported.
type Limit struct {
	Agent        string    `json:"agent"`
	Window       string    `json:"window"` // "5h", "7d", …
	LimitID      string    `json:"limit_id,omitempty"`
	UsedPercent  *float64  `json:"used_percent,omitempty"` // absent when the agent doesn't say
	LimitReached bool      `json:"limit_reached,omitempty"`
	ResetsAt     time.Time `json:"resets_at,omitzero"`
	PlanType     string    `json:"plan_type,omitempty"`
	At           time.Time `json:"at"` // when it was reported
}

// FailureCounts are failed and denied calls, retry loops and sessions that
// ended badly.
type FailureCounts struct {
	ToolFailures  int `json:"tool_failures"`
	ShellFailures int `json:"shell_failures"` // non-zero exit codes
	Denials       int `json:"denials"`
	RetryLoops    int `json:"retry_loops"`
	EndedBadly    int `json:"ended_badly"` // sessions
}

// FailureReport is the period's failures with the tools and programs that
// failed most.
type FailureReport struct {
	FailureCounts
	Tools []FailedTool    `json:"tools"`
	Shell []FailedProgram `json:"shell"`
}

// FailedTool is one tool of one agent that failed or was denied.
type FailedTool struct {
	Agent    string `json:"agent"`
	Tool     string `json:"tool"` // the agent's own name
	Failures int    `json:"failures"`
	Denials  int    `json:"denials"`
}

// FailedProgram is one program that exited with one non-zero code.
type FailedProgram struct {
	Program  string `json:"program"` // "" when it couldn't be told
	ExitCode int    `json:"exit_code"`
	Failures int    `json:"failures"`
}

// build sums the folded sessions into r. Sessions belong to the period
// their top-level session started in, as on the Insights page.
func build(r *Report, f *fold, opt Options) {
	r.Schema = SchemaVersion
	all := f.eng.Sessions()
	roots := map[string]*engine.Session{}
	for _, s := range all {
		if s.ParentID == "" {
			roots[s.ID] = s
		}
	}
	keep := func(s *engine.Session) bool {
		root := roots[s.RootID]
		if root == nil {
			root = s
		}
		if root.StartedAt.Before(opt.Since) || root.StartedAt.After(opt.Now) {
			return false
		}
		if opt.Project == "" {
			return true
		}
		p := f.projects[root.ProjectID]
		return strings.EqualFold(opt.Project, p.Name) || opt.Project == p.ID
	}
	agents := map[string]*AgentUsage{}
	projectRows := map[string]*ProjectRow{}
	models := map[string]*ModelRow{}
	var entries []insights.Entry
	for _, s := range all {
		if !keep(s) {
			continue
		}
		a := agents[s.Agent]
		if a == nil {
			a = &AgentUsage{Agent: s.Agent}
			agents[s.Agent] = a
		}
		for _, u := range []*Usage{&r.Totals, &a.Usage} {
			u.add(s)
		}
		entries = append(entries, insights.Entry{Root: s.RootID, Agent: s.Agent, Project: s.ProjectID, IsRoot: s.ParentID == "", At: s.LastEventAt, Tally: s.Failures})
		if s.Model != "" && !strings.HasPrefix(s.Model, "<") {
			m := models[s.Model]
			if m == nil {
				m = &ModelRow{Model: s.Model}
				models[s.Model] = m
			}
			m.Actors++
			m.InputTokens += s.InputTokens
			m.OutputTokens += s.OutputTokens
			m.CacheReadTokens += s.CacheReadTokens
			m.CacheWriteTokens += s.CacheWriteTokens
			m.CostUSD += s.CostUSD
		}
		if s.ParentID != "" {
			continue
		}
		p := projectRows[s.ProjectID]
		if p == nil {
			info := f.projects[s.ProjectID]
			p = &ProjectRow{ID: s.ProjectID, Name: cmp.Or(info.Name, "Unsorted")}
			if p.ID == "" {
				p.ID = "unsorted"
			}
			projectRows[s.ProjectID] = p
		}
		p.Sessions++
		p.ActiveMS += s.ActiveMS
		p.CostUSD += s.BestCostUSD
	}

	fr := insights.Report(entries)
	r.Failures.FailureCounts = FailureCounts{fr.ToolFailures, fr.ShellFailures, fr.Denials, fr.RetryLoops, fr.EndedBadly}
	for _, sc := range fr.Agents {
		if a := agents[sc.Key]; a != nil {
			a.Failures = FailureCounts{sc.ToolFailures, sc.ShellFailures, sc.Denials, sc.RetryLoops, sc.EndedBadly}
		}
	}
	r.Failures.Tools = []FailedTool{}
	for _, t := range fr.Tools {
		r.Failures.Tools = append(r.Failures.Tools, FailedTool{Agent: t.Agent, Tool: t.ToolRaw, Failures: t.Failures, Denials: t.Denials})
	}
	r.Failures.Shell = []FailedProgram{}
	for _, s := range fr.Shell {
		r.Failures.Shell = append(r.Failures.Shell, FailedProgram{Program: s.Program, ExitCode: s.ExitCode, Failures: s.Failures})
	}

	r.Agents = values(agents, func(a, b *AgentUsage) int {
		return cmp.Or(cmp.Compare(b.CostUSD, a.CostUSD), cmp.Compare(b.Sessions, a.Sessions), cmp.Compare(a.Agent, b.Agent))
	})
	r.Projects = values(projectRows, func(a, b *ProjectRow) int {
		return cmp.Or(cmp.Compare(b.CostUSD, a.CostUSD), cmp.Compare(b.Sessions, a.Sessions), cmp.Compare(a.ID, b.ID))
	})
	r.Models = values(models, func(a, b *ModelRow) int {
		return cmp.Or(cmp.Compare(b.CostUSD, a.CostUSD), cmp.Compare(b.OutputTokens, a.OutputTokens), cmp.Compare(a.Model, b.Model))
	})
	r.Limits = []Limit{}
	for _, e := range f.limits {
		l := Limit{Agent: e.Agent.Name, Window: str(e.Data, "limit_window"), LimitID: str(e.Data, "limit_id"), PlanType: str(e.Data, "plan_type"), At: e.TS}
		if v, ok := e.Data["used_percent"].(float64); ok {
			l.UsedPercent = &v
		}
		l.LimitReached, _ = e.Data["limit_reached"].(bool)
		l.ResetsAt, _ = time.Parse(time.RFC3339, str(e.Data, "resets_at"))
		r.Limits = append(r.Limits, l)
	}
	slices.SortFunc(r.Limits, func(a, b Limit) int {
		return cmp.Or(cmp.Compare(a.Agent, b.Agent), cmp.Compare(a.LimitID, b.LimitID), cmp.Compare(windowMinutes(a.Window), windowMinutes(b.Window)))
	})
}

// add counts one session or subagent.
func (u *Usage) add(s *engine.Session) {
	u.InputTokens += s.InputTokens
	u.OutputTokens += s.OutputTokens
	u.CacheReadTokens += s.CacheReadTokens
	u.CacheWriteTokens += s.CacheWriteTokens
	u.ToolCalls += s.ToolCalls
	if s.ParentID != "" {
		u.Subagents++
		return
	}
	u.Sessions++
	u.Turns += s.Turns
	u.ActiveMS += s.ActiveMS
	u.CostUSD += s.BestCostUSD
	switch {
	case s.CostSource == "reported":
		u.ReportedCostUSD += s.BestCostUSD
	case s.CostSource == "computed":
		u.ComputedCostUSD += s.BestCostUSD
	case s.Usage == engine.UsageTokens:
		u.Unpriced++
	case s.Usage == engine.UsageNone:
		u.NoUsage++
	}
}

// windowMinutes orders windows by length ("5h" before "7d").
func windowMinutes(w string) int {
	if len(w) < 2 {
		return 0
	}
	n := 0
	for _, c := range w[:len(w)-1] {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	switch w[len(w)-1] {
	case 'd':
		return n * 1440
	case 'h':
		return n * 60
	}
	return n
}

func values[T any](m map[string]*T, order func(a, b *T) int) []T {
	list := make([]*T, 0, len(m))
	for _, v := range m {
		list = append(list, v)
	}
	slices.SortFunc(list, order)
	out := make([]T, len(list))
	for i, v := range list {
		out[i] = *v
	}
	return out
}
