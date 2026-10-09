// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package engine

import (
	"path"
	"sort"
	"strings"
	"time"

	"github.com/elephaant/shiplino/pkg/model"
)

// Status is a session's live state. It drives the board columns.
type Status string

const (
	StatusRunning Status = "running"
	StatusWaiting Status = "waiting"
	StatusIdle    Status = "idle"
	StatusReview  Status = "review"
	StatusDone    Status = "done"
	StatusFailed  Status = "failed"
)

// Session is the materialized state of one actor: a top-level agent
// session or one of its subagents (ParentID set).
type Session struct {
	ID           string    `json:"id"`
	Agent        string    `json:"agent"`
	AgentVersion string    `json:"agent_version,omitempty"`
	ParentID     string    `json:"parent_id,omitempty"`
	RootID       string    `json:"root_id"`
	ActorType    string    `json:"actor_type,omitempty"`
	Depth        int       `json:"depth"`
	CWD          string    `json:"cwd,omitempty"`
	ProjectID    string    `json:"project_id,omitempty"`
	Branch       string    `json:"branch,omitempty"`
	Title        string    `json:"title,omitempty"`
	TitleSource  string    `json:"title_source,omitempty"` // "agent" or "prompt"
	Model        string    `json:"model,omitempty"`
	Status       Status    `json:"status"`
	NowDoing     string    `json:"now_doing,omitempty"`
	StartedAt    time.Time `json:"started_at"`
	EndedAt      time.Time `json:"ended_at,omitzero"`
	LastEventAt  time.Time `json:"last_event_at"`

	Turns        int      `json:"turns"`
	ToolCalls    int      `json:"tool_calls"`
	ToolErrors   int      `json:"tool_errors"`
	Files        []string `json:"files,omitempty"` // distinct paths edited, sorted
	LinesAdded   int      `json:"lines_added"`
	LinesRemoved int      `json:"lines_removed"`

	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	CostUSD          float64 `json:"cost_usd"` // computed from this actor's own responses

	// TreeCostUSD is CostUSD plus every descendant subagent's CostUSD.
	TreeCostUSD float64 `json:"tree_cost_usd"`
	// ReportedCostUSD is the agent's own cost accounting attributed to this
	// session (root sessions only). It includes calls the transcript never
	// shows, so when present it is the better figure.
	ReportedCostUSD float64 `json:"reported_cost_usd,omitempty"`
	// BestCostUSD and CostSource are what to display: "reported" or "computed".
	BestCostUSD float64 `json:"best_cost_usd"`
	CostSource  string  `json:"cost_source,omitempty"`

	Links []Link `json:"links,omitempty"` // PRs and pushes the agent reported

	LastGitCommitAt time.Time `json:"last_git_commit_at,omitzero"` // agent ran `git commit` itself
	WaitingMS       int64     `json:"waiting_ms"`
	WaitingSince    time.Time `json:"waiting_since,omitzero"`
	TranscriptPath  string    `json:"transcript_path,omitempty"`
	// HookSeen is set once the session has events from the agent's hooks.
	// Activity then comes from hooks; the transcript adds usage and titles.
	HookSeen bool `json:"hook_seen,omitempty"`
}

// Link is an external object the session produced, e.g. a pull request.
type Link struct {
	Kind    string `json:"kind"` // "pr" | "push" | "commit"
	URL     string `json:"url,omitempty"`
	Number  int    `json:"number,omitempty"`
	Ref     string `json:"ref,omitempty"` // branch for pushes, sha for commits
	Action  string `json:"action,omitempty"`
	Message string `json:"message,omitempty"` // commit subject
}

// setTitle applies the precedence rule: the agent's own title always
// wins; a title derived from the prompt only fills an empty one.
func (s *Session) setTitle(title, source string) {
	if title == "" {
		return
	}
	if source == "agent" || s.Title == "" {
		s.Title, s.TitleSource = title, source
	}
}

// FilesChanged is the number of distinct files the session edited.
func (s *Session) FilesChanged() int { return len(s.Files) }

// Engine folds events into sessions. It is not safe for concurrent use;
// the daemon feeds it from a single goroutine.
type Engine struct {
	sessions map[string]*Session
	// processTotals is the last running total seen per agent process, so a
	// process that spans several sessions is split between them.
	processTotals map[string]float64
}

// New returns an engine primed with previously stored sessions and the
// last reported total per agent process (nil if none).
func New(existing []*Session, processTotals map[string]float64) *Engine {
	e := &Engine{sessions: make(map[string]*Session, len(existing)), processTotals: map[string]float64{}}
	for _, s := range existing {
		e.sessions[s.ID] = s
	}
	for k, v := range processTotals {
		e.processTotals[k] = v
	}
	return e
}

// Get returns the session with id, or nil.
func (e *Engine) Get(id string) *Session { return e.sessions[id] }

// Apply folds one event into state and returns the sessions it changed.
// Callers must apply each event at most once (dedup happens in the store).
func (e *Engine) Apply(ev model.Event) []*Session {
	actorID := ev.ActorID
	if actorID == "" {
		actorID = ev.SessionID
	}
	s := e.ensure(actorID, ev)
	changed := []*Session{s}
	if ev.TS.After(s.LastEventAt) {
		s.LastEventAt = ev.TS
	}
	if ev.Agent.Version != "" {
		s.AgentVersion = ev.Agent.Version
	}
	if ev.Collector == model.CollectorHook {
		s.HookSeen = true
	}
	if ev.Project != nil {
		// A session stays in the project it started in (doc: edge cases).
		setIfEmpty(&s.ProjectID, ev.Project.ID)
		if ev.Project.Branch != "" {
			s.Branch = ev.Project.Branch
		}
	}

	switch ev.Kind {
	case model.KindSessionStart:
		s.Status = StatusRunning
		s.NowDoing = ""
		s.setTitle(str(ev.Data, "title"), "agent")
		setIfEmpty(&s.Model, str(ev.Data, "model"))
		setIfEmpty(&s.TranscriptPath, str(ev.Data, "transcript_path"))

	case model.KindTurnStart:
		e.endWaiting(s, ev.TS)
		s.Status = StatusRunning
		s.Turns++
		s.NowDoing = "Thinking…"
		if t := str(ev.Data, "title"); t != "" {
			s.setTitle(t, "agent")
		} else {
			s.setTitle(TitleFromPrompt(str(ev.Data, "prompt")), "prompt")
		}
		setIfEmpty(&s.TranscriptPath, str(ev.Data, "transcript_path"))

	case model.KindToolStart:
		e.endWaiting(s, ev.TS)
		s.Status = StatusRunning
		s.ToolCalls++
		s.NowDoing = nowDoing(str(ev.Data, "tool"), str(ev.Data, "tool_raw"), str(ev.Data, "input_summary"))

	case model.KindToolEnd:
		e.endWaiting(s, ev.TS)
		if s.Status != StatusFailed {
			s.Status = StatusRunning
		}
		if ok, _ := ev.Data["ok"].(bool); !ok {
			s.ToolErrors++
		}

	case model.KindFileEdit:
		if p := str(ev.Data, "path"); p != "" {
			s.Files = addSorted(s.Files, p)
		}
		s.LinesAdded += num(ev.Data, "lines_added")
		s.LinesRemoved += num(ev.Data, "lines_removed")

	case model.KindWaitingStart:
		if s.Status != StatusWaiting {
			s.WaitingSince = ev.TS
		}
		s.Status = StatusWaiting
		s.NowDoing = str(ev.Data, "message")

	case model.KindWaitingEnd:
		e.endWaiting(s, ev.TS)
		s.Status = StatusRunning

	case model.KindTurnEnd:
		e.endWaiting(s, ev.TS)
		s.NowDoing = ""
		switch {
		case str(ev.Data, "status") == "error":
			s.Status = StatusFailed
			s.NowDoing = str(ev.Data, "error")
		case len(s.Files) > 0:
			s.Status = StatusReview
		default:
			s.Status = StatusDone
		}

	case model.KindSubagentStart:
		child := e.child(s, str(ev.Data, "child_session_id"), str(ev.Data, "agent_type"), ev)
		child.Status = StatusRunning
		changed = append(changed, child)

	case model.KindSubagentEnd:
		child := e.child(s, str(ev.Data, "child_session_id"), str(ev.Data, "agent_type"), ev)
		e.endWaiting(child, ev.TS)
		child.Status = StatusDone
		child.NowDoing = ""
		child.EndedAt = ev.TS
		setIfEmpty(&child.TranscriptPath, str(ev.Data, "transcript_path"))
		changed = append(changed, child)

	case model.KindUsage:
		if report, _ := ev.Data["report"].(bool); report {
			proc := str(ev.Data, "process")
			total, _ := ev.Data["total_cost_usd"].(float64)
			if d := total - e.processTotals[proc]; d > 0 {
				s.ReportedCostUSD += d
				e.processTotals[proc] = total
			}
			s.updateBestCost()
			break
		}
		s.InputTokens += int64(num(ev.Data, "input_tokens"))
		s.OutputTokens += int64(num(ev.Data, "output_tokens"))
		s.CacheReadTokens += int64(num(ev.Data, "cache_read_tokens"))
		s.CacheWriteTokens += int64(num(ev.Data, "cache_write_tokens"))
		if c, ok := ev.Data["cost_usd"].(float64); ok && c > 0 {
			s.CostUSD += c
			// Roll the cost up the actor tree.
			for a := s; a != nil; a = e.sessions[a.ParentID] {
				a.TreeCostUSD += c
				a.updateBestCost()
				if a != s {
					changed = append(changed, a)
				}
				if a.ParentID == "" {
					break
				}
			}
		}
		// The model that answered the latest response is the current one
		// (sessions can switch). "<synthetic>" marks agent placeholders.
		if m := str(ev.Data, "model"); m != "" && !strings.HasPrefix(m, "<") {
			s.Model = m
		}

	case model.KindSessionUpdate:
		s.setTitle(str(ev.Data, "title"), "agent")
		setIfEmpty(&s.Model, str(ev.Data, "model"))

	case model.KindShellExec:
		if code, ok := ev.Data["exit_code"]; ok && num(ev.Data, "exit_code") == 0 && code != nil && isGitCommit(str(ev.Data, "command")) {
			s.LastGitCommitAt = ev.TS
		}

	case model.KindGitCommit:
		s.addLink(Link{Kind: "commit", Ref: str(ev.Data, "sha"), Message: str(ev.Data, "message"), Action: str(ev.Data, "attribution")})
		// Committed work is done (the plan's default rule: Review → Done on commit).
		if s.Status == StatusReview {
			s.Status = StatusDone
		}

	case model.KindGitPR:
		s.addLink(Link{Kind: "pr", URL: str(ev.Data, "url"), Number: num(ev.Data, "number"), Action: str(ev.Data, "action")})

	case model.KindGitPush:
		s.addLink(Link{Kind: "push", Ref: str(ev.Data, "branch")})

	case model.KindSessionEnd:
		e.endWaiting(s, ev.TS)
		s.EndedAt = ev.TS
		s.NowDoing = ""
		if s.Status == StatusRunning || s.Status == StatusWaiting || s.Status == StatusIdle {
			s.Status = StatusDone
		}
	}
	return changed
}

// Redundant reports whether ev is transcript-derived activity for a
// session whose hooks already report that activity. Transcript and hook
// ids for the same tool call can differ, so both would double count.
// Usage, cost reports and titles are never redundant.
func (e *Engine) Redundant(ev model.Event) bool {
	if ev.Collector != model.CollectorTranscript {
		return false
	}
	switch ev.Kind {
	case model.KindSessionStart, model.KindTurnStart, model.KindTurnEnd,
		model.KindToolStart, model.KindToolEnd, model.KindShellExec,
		model.KindFileEdit, model.KindMCPCall:
	default:
		return false
	}
	s := e.sessions[ev.SessionID]
	return s != nil && s.HookSeen
}

func (e *Engine) ensure(id string, ev model.Event) *Session {
	if s, ok := e.sessions[id]; ok {
		return s
	}
	s := &Session{ID: id, Agent: ev.Agent.Name, RootID: ev.SessionID, Status: StatusRunning, StartedAt: ev.TS, LastEventAt: ev.TS}
	if ev.Project != nil {
		s.CWD = ev.Project.CWD
	}
	if id != ev.SessionID {
		s.ParentID = ev.ParentActor
		if s.ParentID == "" {
			s.ParentID = ev.SessionID
		}
		s.ActorType = ev.ActorType
		s.Depth = e.depthOf(s.ParentID) + 1
		if p := e.sessions[s.ParentID]; p != nil {
			s.ProjectID, s.Branch = p.ProjectID, p.Branch // subagents work in the parent's project
		}
	}
	e.sessions[id] = s
	return s
}

func (e *Engine) child(parent *Session, id, actorType string, ev model.Event) *Session {
	if id == "" {
		id = parent.ID + "/sub:unknown"
	}
	if c, ok := e.sessions[id]; ok {
		setIfEmpty(&c.ActorType, actorType)
		return c
	}
	c := &Session{
		ID: id, Agent: parent.Agent, ParentID: parent.ID, RootID: parent.RootID, ActorType: actorType,
		Depth: parent.Depth + 1, CWD: parent.CWD, ProjectID: parent.ProjectID, Branch: parent.Branch,
		Status: StatusRunning, StartedAt: ev.TS, LastEventAt: ev.TS,
	}
	e.sessions[id] = c
	return c
}

func (e *Engine) depthOf(id string) int {
	if s, ok := e.sessions[id]; ok {
		return s.Depth
	}
	return 0
}

func (e *Engine) endWaiting(s *Session, at time.Time) {
	if s.Status != StatusWaiting || s.WaitingSince.IsZero() {
		return
	}
	if d := at.Sub(s.WaitingSince); d > 0 {
		s.WaitingMS += d.Milliseconds()
	}
	s.WaitingSince = time.Time{}
}

// isGitCommit reports whether a shell command creates a commit: `git`
// must be in command position (start, or after && || ; | ( and the like),
// so `echo git commit` doesn't count.
func isGitCommit(cmd string) bool {
	for _, sep := range []string{"&&", "||", ";", "|", "(", ")", "\n"} {
		cmd = strings.ReplaceAll(cmd, sep, " \x00 ")
	}
	f := strings.Fields(cmd)
	for i := 0; i+1 < len(f); i++ {
		start := i == 0 || f[i-1] == "\x00" || f[i-1] == "then" || f[i-1] == "do" || f[i-1] == "sudo"
		if start && (f[i] == "git" || strings.HasSuffix(f[i], "/git")) && hasCommitVerb(f[i+1:]) {
			return true
		}
	}
	return false
}

// hasCommitVerb skips git's global options (-C dir, -c k=v) to the verb.
func hasCommitVerb(args []string) bool {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-C" || a == "-c":
			i++
		case strings.HasPrefix(a, "-"):
		default:
			return a == "commit"
		}
	}
	return false
}

// addLink records a link once (by kind + URL/ref), keeping the latest action.
func (s *Session) addLink(l Link) {
	for i, x := range s.Links {
		if x.Kind == l.Kind && x.URL == l.URL && x.Ref == l.Ref {
			s.Links[i].Action = l.Action
			return
		}
	}
	s.Links = append(s.Links, l)
}

// updateBestCost picks the figure to display. The agent's own report
// covers background calls the transcript lacks, so it wins when larger.
func (s *Session) updateBestCost() {
	switch {
	case s.ReportedCostUSD > 0 && s.ReportedCostUSD >= s.TreeCostUSD:
		s.BestCostUSD, s.CostSource = s.ReportedCostUSD, "reported"
	case s.TreeCostUSD > 0:
		s.BestCostUSD, s.CostSource = s.TreeCostUSD, "computed"
	}
}

// ProcessTotals returns the last reported total per agent process.
func (e *Engine) ProcessTotals() map[string]float64 {
	out := make(map[string]float64, len(e.processTotals))
	for k, v := range e.processTotals {
		out[k] = v
	}
	return out
}

// TitleFromPrompt makes a card title from a prompt without an LLM: the
// first sentence of the first line, at most 80 characters.
func TitleFromPrompt(p string) string {
	p = strings.TrimSpace(p)
	if i := strings.IndexByte(p, '\n'); i >= 0 {
		p = p[:i]
	}
	for _, sep := range []string{". ", "? ", "! "} {
		if i := strings.Index(p, sep); i > 0 {
			p = p[:i+1]
		}
	}
	p = strings.Join(strings.Fields(p), " ")
	if r := []rune(p); len(r) > 80 {
		p = strings.TrimSpace(string(r[:79])) + "…"
	}
	return p
}

func nowDoing(tool, raw, summary string) string {
	short := summary
	if tool == model.ToolEdit || tool == model.ToolWrite || tool == model.ToolRead {
		short = path.Base(strings.ReplaceAll(summary, "\\", "/"))
	}
	switch tool {
	case model.ToolEdit, model.ToolWrite:
		return "Editing " + short
	case model.ToolRead:
		return "Reading " + short
	case model.ToolShell:
		return "Running " + short
	case model.ToolSearch:
		return "Searching " + short
	case model.ToolWeb:
		return "Browsing " + short
	case model.ToolMCP:
		return "Calling " + short
	case model.ToolTask:
		return "Delegating: " + short
	}
	return "Using " + raw
}

func addSorted(list []string, v string) []string {
	i := sort.SearchStrings(list, v)
	if i < len(list) && list[i] == v {
		return list
	}
	list = append(list, "")
	copy(list[i+1:], list[i:])
	list[i] = v
	return list
}

func setIfEmpty(dst *string, v string) {
	if *dst == "" {
		*dst = v
	}
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

// num reads an integer that may have been decoded from JSON as float64.
func num(m map[string]any, k string) int {
	switch v := m[k].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return 0
}
