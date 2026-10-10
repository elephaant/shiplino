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
	// Telemetry is the per-request usage the agent exported itself over
	// OpenTelemetry (background calls included). It describes the same API
	// requests as transcript usage and the agent's running total, so it is
	// compared with them and never added to them.
	Telemetry *Telemetry `json:"telemetry,omitempty"`
	// TokensSource is "telemetry" while the token counts come from
	// Telemetry because the session has no direct (transcript, hook or
	// ingested) usage yet. Direct usage replaces them as soon as it arrives.
	TokensSource string `json:"tokens_source,omitempty"`
	// BestCostUSD and CostSource are what to display: "reported" or "computed".
	BestCostUSD float64 `json:"best_cost_usd"`
	CostSource  string  `json:"cost_source,omitempty"`
	// Usage says whether token usage was recorded for this session or its
	// subagents: UsageTokens once any arrives, UsageNone when the session
	// has settled after doing work without any (the agent records none,
	// e.g. Cursor transcripts or Windsurf hooks), and "" until then.
	Usage string `json:"usage,omitempty"`

	Links []Link `json:"links,omitempty"` // PRs and pushes the agent reported

	LastGitCommitAt time.Time `json:"last_git_commit_at,omitzero"` // agent ran `git commit` itself
	WaitingMS       int64     `json:"waiting_ms"`
	// ActiveMS is time spent in turns (prompt to answer), the agent's own
	// turn duration when it reports one. Idle time between turns isn't
	// counted.
	ActiveMS      int64     `json:"active_ms"`
	TurnStartedAt time.Time `json:"turn_started_at,omitzero"`
	// InFlight is the number of tool calls started and not yet finished.
	InFlight       int       `json:"in_flight,omitempty"`
	WaitingSince   time.Time `json:"waiting_since,omitzero"`
	TranscriptPath string    `json:"transcript_path,omitempty"`
	// HookSeen is set once the session has events from the agent's hooks.
	// Activity then comes from hooks; the transcript adds usage and titles.
	HookSeen bool `json:"hook_seen,omitempty"`
	// ActivitySource is the collector of the session's first activity
	// event (turns, tools). Telemetry activity only fills sessions that
	// nothing else covers.
	ActivitySource string `json:"activity_source,omitempty"`
}

// Telemetry sums the usage records an agent exported over OpenTelemetry.
type Telemetry struct {
	Requests         int     `json:"requests"`
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	CostUSD          float64 `json:"cost_usd"`
}

// Link is an external object the session produced, e.g. a pull request.
type Link struct {
	Kind    string `json:"kind"` // "pr" | "push" | "commit"
	URL     string `json:"url,omitempty"`
	Number  int    `json:"number,omitempty"`
	Ref     string `json:"ref,omitempty"` // branch for pushes, sha for commits
	Action  string `json:"action,omitempty"`
	Message string `json:"message,omitempty"` // commit subject

	// Pull request state, filled in by the API from the GitHub
	// integration when it's on (never by the engine).
	State  string `json:"state,omitempty"`  // open | draft | merged | closed
	Checks string `json:"checks,omitempty"` // success | failure | pending
	Review string `json:"review,omitempty"` // approved | changes_requested
	Title  string `json:"title,omitempty"`
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

// Values of Session.Usage.
const (
	UsageTokens = "tokens"
	UsageNone   = "none"
)

// refreshUsage marks a settled session that did work without any usage.
func (s *Session) refreshUsage() {
	if s.Usage == UsageTokens {
		return
	}
	s.Usage = ""
	switch s.Status {
	case StatusIdle, StatusReview, StatusDone, StatusFailed:
		if s.Turns > 0 || s.ToolCalls > 0 {
			s.Usage = UsageNone
		}
	}
}

// FilesChanged is the number of distinct files the session edited.
func (s *Session) FilesChanged() int { return len(s.Files) }

// Rev identifies the engine's folding rules. Bump it whenever Apply would
// produce different sessions from the same events (a new field, a fix);
// the daemon then rebuilds stored sessions from their events once.
const Rev = 6

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
	if activityKinds[ev.Kind] && s.ActivitySource == "" {
		s.ActivitySource = string(ev.Collector)
	}
	if ev.Project != nil {
		// A session stays in the project it started in (doc: edge cases).
		setIfEmpty(&s.ProjectID, ev.Project.ID)
		// Agents without session start hooks (Windsurf) name a folder later.
		setIfEmpty(&s.CWD, ev.Project.CWD)
		if ev.Project.Branch != "" {
			s.Branch = ev.Project.Branch
		}
	}

	// An event from before the session ended (e.g. read late from another
	// file) still counts, but doesn't reopen the session.
	late := !s.EndedAt.IsZero() && !ev.TS.After(s.EndedAt)
	prevStatus, prevNow := s.Status, s.NowDoing

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
		s.TurnStartedAt = ev.TS
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
		s.InFlight++
		s.NowDoing = nowDoing(str(ev.Data, "tool"), str(ev.Data, "tool_raw"), str(ev.Data, "input_summary"))

	case model.KindToolEnd:
		e.endWaiting(s, ev.TS)
		if s.InFlight > 0 {
			s.InFlight--
		}
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
		switch d := int64(num(ev.Data, "duration_ms")); {
		case d > 0:
			s.ActiveMS += d
		case !s.TurnStartedAt.IsZero() && ev.TS.After(s.TurnStartedAt):
			s.ActiveMS += ev.TS.Sub(s.TurnStartedAt).Milliseconds()
		}
		s.TurnStartedAt = time.Time{}
		s.InFlight = 0
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
		child.InFlight = 0
		child.EndedAt = ev.TS
		setIfEmpty(&child.TranscriptPath, str(ev.Data, "transcript_path"))
		changed = append(changed, child)

	case model.KindUsage:
		// The session and its ancestors have usage data, even when unpriced.
		for a := s; a != nil && a.Usage != UsageTokens; a = e.sessions[a.ParentID] {
			a.Usage = UsageTokens
			if a != s {
				changed = append(changed, a)
			}
			if a.ParentID == "" {
				break
			}
		}
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
		if ev.Collector == model.CollectorOTLP {
			s.addTelemetry(ev)
			break
		}
		if s.TokensSource == "telemetry" { // direct usage replaces the stand-in
			s.InputTokens, s.OutputTokens, s.CacheReadTokens, s.CacheWriteTokens = 0, 0, 0, 0
			s.TokensSource = ""
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
		// A merged PR is shipped work, like a commit: Review → Done.
		if str(ev.Data, "action") == "merged" && s.Status == StatusReview {
			s.Status = StatusDone
		}

	case model.KindGitPush:
		s.addLink(Link{Kind: "push", Ref: str(ev.Data, "branch")})

	case model.KindSessionEnd:
		e.endWaiting(s, ev.TS)
		s.EndedAt = ev.TS
		s.InFlight = 0
		s.NowDoing = ""
		if s.Status == StatusRunning || s.Status == StatusWaiting || s.Status == StatusIdle {
			s.Status = StatusDone
		}
	}
	if late && ev.Kind != model.KindSessionEnd {
		s.Status, s.NowDoing = prevStatus, prevNow
	}
	for _, c := range changed {
		c.refreshUsage()
	}
	return changed
}

// Quiet periods after which a running session counts as idle (the agent
// was likely closed without saying so). A tool still running, such as a
// long build, gets longer.
const (
	IdleAfter         = 30 * time.Minute
	IdleAfterInFlight = 2 * time.Hour
)

// MarkIdle sets running sessions that have been quiet too long to idle and
// returns them. Waiting sessions stay waiting: they really need the user.
// The next event brings a session back.
func (e *Engine) MarkIdle(now time.Time) []*Session {
	var out []*Session
	for _, s := range e.sessions {
		if s.Status != StatusRunning {
			continue
		}
		after := IdleAfter
		if s.InFlight > 0 {
			after = IdleAfterInFlight
		}
		if now.Sub(s.LastEventAt) >= after {
			s.Status, s.NowDoing = StatusIdle, ""
			s.refreshUsage()
			out = append(out, s)
		}
	}
	return out
}

// activityKinds are what hooks, transcripts and telemetry all describe,
// each with ids of their own.
var activityKinds = map[model.Kind]bool{
	model.KindSessionStart: true, model.KindTurnStart: true, model.KindTurnEnd: true,
	model.KindToolStart: true, model.KindToolEnd: true, model.KindShellExec: true,
	model.KindFileEdit: true, model.KindMCPCall: true,
}

// Redundant reports whether ev is activity that another source already
// reports for its session: transcript activity once hooks are seen, and
// telemetry (OTLP) activity once hooks or transcripts are. Their ids for
// the same tool call or turn can differ, so both would double count.
// Usage, cost reports and titles are never redundant.
func (e *Engine) Redundant(ev model.Event) bool {
	if !activityKinds[ev.Kind] {
		return false
	}
	s := e.sessions[ev.SessionID]
	if s == nil {
		return false
	}
	switch ev.Collector {
	case model.CollectorTranscript:
		return s.HookSeen
	case model.CollectorOTLP:
		return s.HookSeen || (s.ActivitySource != "" && s.ActivitySource != string(model.CollectorOTLP))
	}
	return false
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

// addTelemetry folds one OTLP usage record into Telemetry. Its tokens
// stand in for the session's own counts only until direct usage arrives.
// Its model only fills an empty one: telemetry includes background calls
// to small models, which aren't the session's model.
func (s *Session) addTelemetry(ev model.Event) {
	t := s.Telemetry
	if t == nil {
		t = &Telemetry{}
		s.Telemetry = t
	}
	t.Requests++
	t.InputTokens += int64(num(ev.Data, "input_tokens"))
	t.OutputTokens += int64(num(ev.Data, "output_tokens"))
	t.CacheReadTokens += int64(num(ev.Data, "cache_read_tokens"))
	t.CacheWriteTokens += int64(num(ev.Data, "cache_write_tokens"))
	if c, ok := ev.Data["cost_usd"].(float64); ok && c > 0 {
		t.CostUSD += c
	}
	if s.TokensSource == "telemetry" || s.InputTokens+s.OutputTokens+s.CacheReadTokens+s.CacheWriteTokens == 0 {
		s.InputTokens, s.OutputTokens = t.InputTokens, t.OutputTokens
		s.CacheReadTokens, s.CacheWriteTokens = t.CacheReadTokens, t.CacheWriteTokens
		s.TokensSource = "telemetry"
	}
	if m := str(ev.Data, "model"); !strings.HasPrefix(m, "<") {
		setIfEmpty(&s.Model, m)
	}
	s.updateBestCost()
}

// updateBestCost picks the figure to display. The agent's own accounting
// (its running total, or the per-request cost it exports as telemetry)
// covers background calls the transcript lacks, so it wins when larger.
// Both agent figures count the same calls: the larger is used, never
// their sum.
func (s *Session) updateBestCost() {
	reported := s.ReportedCostUSD
	if s.Telemetry != nil {
		reported = max(reported, s.Telemetry.CostUSD)
	}
	switch {
	case reported > 0 && reported >= s.TreeCostUSD:
		s.BestCostUSD, s.CostSource = reported, "reported"
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
