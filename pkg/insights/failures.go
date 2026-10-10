package insights

import (
	"cmp"
	"slices"
	"time"
)

// maxRows caps each list of the report; maxLinks caps the sessions linked
// from a row.
const (
	maxRows  = 20
	maxLinks = 20
)

// Entry is one session or subagent with its tally.
type Entry struct {
	Root    string // the root session's id: what rows link to
	Agent   string
	Project string
	IsRoot  bool      // the main session (its ending counts), not a subagent
	At      time.Time // last activity: the most recent sessions are linked first
	Tally   *Tally
}

// Links are the sessions behind a row, most recent first.
type Links struct {
	Count int      `json:"session_count"`
	IDs   []string `json:"sessions"`
}

// ToolRow is one tool of one agent that failed or was denied.
type ToolRow struct {
	Agent    string   `json:"agent"`
	Tool     string   `json:"tool"`
	ToolRaw  string   `json:"tool_raw"`
	Failures int      `json:"failures"`
	Denials  int      `json:"denials"`
	Projects []string `json:"projects"`
	Links
}

// ShellRow is one program that exited with one non-zero code.
type ShellRow struct {
	Program  string   `json:"program"` // "" when it couldn't be told
	ExitCode int      `json:"exit_code"`
	Failures int      `json:"failures"`
	Agents   []string `json:"agents"`
	Projects []string `json:"projects"`
	Links
}

// LoopRow is one retry loop of one session.
type LoopRow struct {
	Session string `json:"session"`
	Agent   string `json:"agent"`
	Project string `json:"project"`
	Loop
}

// Ending is a session whose last turn ended on an error or was
// interrupted, or that ended on an error itself.
type Ending struct {
	Session string    `json:"session"`
	Agent   string    `json:"agent"`
	Project string    `json:"project"`
	Status  string    `json:"status"` // error or interrupted
	At      time.Time `json:"at"`
}

// Scope sums failures for one agent or project.
type Scope struct {
	Key           string `json:"key"`
	Name          string `json:"name,omitempty"`
	ToolFailures  int    `json:"tool_failures"`
	ShellFailures int    `json:"shell_failures"`
	Denials       int    `json:"denials"`
	RetryLoops    int    `json:"retry_loops"`
	EndedBadly    int    `json:"ended_badly"`
	Links
}

// Failures is the failure report for a set of sessions.
type Failures struct {
	ToolFailures  int `json:"tool_failures"`  // tool calls that ran and failed
	ShellFailures int `json:"shell_failures"` // shell commands with a non-zero exit code
	Denials       int `json:"denials"`        // calls and permission prompts refused
	RetryLoops    int `json:"retry_loops"`
	EndedBadly    int `json:"ended_badly"` // sessions

	Tools    []ToolRow  `json:"tools"`
	Shell    []ShellRow `json:"shell"`
	Loops    []LoopRow  `json:"loops"`
	Endings  []Ending   `json:"endings"`
	Agents   []Scope    `json:"agents"`
	Projects []Scope    `json:"projects"`
}

// links collects the sessions of a row with their latest time.
type links map[string]time.Time

func (l links) add(id string, at time.Time) {
	if old, ok := l[id]; !ok || at.After(old) {
		l[id] = at
	}
}

func (l links) out() Links {
	ids := make([]string, 0, len(l))
	for id := range l {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b string) int { return cmp.Or(l[b].Compare(l[a]), cmp.Compare(a, b)) })
	return Links{Count: len(ids), IDs: ids[:min(len(ids), maxLinks)]}
}

type set map[string]bool

func (s set) list() []string {
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

type scopeAcc struct {
	row   Scope
	links links
}

// Report sums the tallies of a period's sessions.
func Report(entries []Entry) Failures {
	var f Failures
	type toolKey struct{ agent, tool, raw string }
	type shellKey struct {
		program string
		code    int
	}
	type toolAcc struct {
		row      ToolRow
		projects set
		links    links
	}
	type shellAcc struct {
		row              ShellRow
		agents, projects set
		links            links
	}
	tools, shells := map[toolKey]*toolAcc{}, map[shellKey]*shellAcc{}
	agents, projects := map[string]*scopeAcc{}, map[string]*scopeAcc{}
	scopes := func(e Entry, fn func(*Scope)) {
		for _, x := range []struct {
			m map[string]*scopeAcc
			k string
		}{{agents, e.Agent}, {projects, e.Project}} {
			a := x.m[x.k]
			if a == nil {
				a = &scopeAcc{row: Scope{Key: x.k}, links: links{}}
				x.m[x.k] = a
			}
			fn(&a.row)
			a.links.add(e.Root, e.At)
		}
	}

	for _, e := range entries {
		t := e.Tally
		if t.Empty() {
			continue
		}
		for _, c := range t.Tools {
			k := toolKey{e.Agent, c.Tool, c.ToolRaw}
			a := tools[k]
			if a == nil {
				a = &toolAcc{row: ToolRow{Agent: e.Agent, Tool: c.Tool, ToolRaw: c.ToolRaw}, projects: set{}, links: links{}}
				tools[k] = a
			}
			a.row.Failures += c.Failures
			a.row.Denials += c.Denials
			a.projects[e.Project] = true
			a.links.add(e.Root, e.At)
			f.ToolFailures += c.Failures
			f.Denials += c.Denials
			scopes(e, func(s *Scope) { s.ToolFailures += c.Failures; s.Denials += c.Denials })
		}
		for _, c := range t.Shell {
			k := shellKey{c.Program, c.ExitCode}
			a := shells[k]
			if a == nil {
				a = &shellAcc{row: ShellRow{Program: c.Program, ExitCode: c.ExitCode}, agents: set{}, projects: set{}, links: links{}}
				shells[k] = a
			}
			a.row.Failures += c.Failures
			a.agents[e.Agent], a.projects[e.Project] = true, true
			a.links.add(e.Root, e.At)
			f.ShellFailures += c.Failures
			scopes(e, func(s *Scope) { s.ShellFailures += c.Failures })
		}
		if t.Denials > 0 {
			f.Denials += t.Denials
			scopes(e, func(s *Scope) { s.Denials += t.Denials })
		}
		for _, l := range t.AllLoops() {
			f.Loops = append(f.Loops, LoopRow{Session: e.Root, Agent: e.Agent, Project: e.Project, Loop: l})
			f.RetryLoops++
			scopes(e, func(s *Scope) { s.RetryLoops++ })
		}
		if e.IsRoot && (t.Ending == "error" || t.Ending == "interrupted") {
			f.Endings = append(f.Endings, Ending{Session: e.Root, Agent: e.Agent, Project: e.Project, Status: t.Ending, At: t.EndedAt})
			f.EndedBadly++
			scopes(e, func(s *Scope) { s.EndedBadly++ })
		}
	}

	for _, a := range tools {
		a.row.Projects, a.row.Links = a.projects.list(), a.links.out()
		f.Tools = append(f.Tools, a.row)
	}
	slices.SortFunc(f.Tools, func(a, b ToolRow) int {
		return cmp.Or(cmp.Compare(b.Failures+b.Denials, a.Failures+a.Denials), cmp.Compare(a.Agent, b.Agent), cmp.Compare(a.ToolRaw, b.ToolRaw))
	})
	for _, a := range shells {
		a.row.Agents, a.row.Projects, a.row.Links = a.agents.list(), a.projects.list(), a.links.out()
		f.Shell = append(f.Shell, a.row)
	}
	slices.SortFunc(f.Shell, func(a, b ShellRow) int {
		return cmp.Or(cmp.Compare(b.Failures, a.Failures), cmp.Compare(a.Program, b.Program), cmp.Compare(a.ExitCode, b.ExitCode))
	})
	slices.SortFunc(f.Loops, func(a, b LoopRow) int {
		return cmp.Or(cmp.Compare(b.Failures, a.Failures), b.Last.Compare(a.Last), cmp.Compare(a.Session, b.Session))
	})
	slices.SortFunc(f.Endings, func(a, b Ending) int { return cmp.Or(b.At.Compare(a.At), cmp.Compare(a.Session, b.Session)) })
	f.Agents, f.Projects = scopeRows(agents), scopeRows(projects)
	f.Tools, f.Shell, f.Loops, f.Endings = top(f.Tools), top(f.Shell), top(f.Loops), top(f.Endings)
	return f
}

// scopeRows lists agents or projects, most failures first.
func scopeRows(m map[string]*scopeAcc) []Scope {
	out := make([]Scope, 0, len(m))
	for _, a := range m {
		a.row.Links = a.links.out()
		out = append(out, a.row)
	}
	total := func(s Scope) int { return s.ToolFailures + s.ShellFailures + s.Denials + s.RetryLoops + s.EndedBadly }
	slices.SortFunc(out, func(a, b Scope) int { return cmp.Or(cmp.Compare(total(b), total(a)), cmp.Compare(a.Key, b.Key)) })
	return out
}

func top[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s[:min(len(s), maxRows)]
}
