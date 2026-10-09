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
	ID          string    `json:"id"`
	Agent       string    `json:"agent"`
	ParentID    string    `json:"parent_id,omitempty"`
	RootID      string    `json:"root_id"`
	ActorType   string    `json:"actor_type,omitempty"`
	Depth       int       `json:"depth"`
	CWD         string    `json:"cwd,omitempty"`
	Title       string    `json:"title,omitempty"`
	Model       string    `json:"model,omitempty"`
	Status      Status    `json:"status"`
	NowDoing    string    `json:"now_doing,omitempty"`
	StartedAt   time.Time `json:"started_at"`
	EndedAt     time.Time `json:"ended_at,omitzero"`
	LastEventAt time.Time `json:"last_event_at"`

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
	CostUSD          float64 `json:"cost_usd"`

	WaitingMS      int64     `json:"waiting_ms"`
	WaitingSince   time.Time `json:"waiting_since,omitzero"`
	TranscriptPath string    `json:"transcript_path,omitempty"`
}

// FilesChanged is the number of distinct files the session edited.
func (s *Session) FilesChanged() int { return len(s.Files) }

// Engine folds events into sessions. It is not safe for concurrent use;
// the daemon feeds it from a single goroutine.
type Engine struct {
	sessions map[string]*Session
}

// New returns an engine primed with previously stored sessions.
func New(existing []*Session) *Engine {
	e := &Engine{sessions: make(map[string]*Session, len(existing))}
	for _, s := range existing {
		e.sessions[s.ID] = s
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

	switch ev.Kind {
	case model.KindSessionStart:
		s.Status = StatusRunning
		s.NowDoing = ""
		setIfEmpty(&s.Title, str(ev.Data, "title"))
		setIfEmpty(&s.Model, str(ev.Data, "model"))
		setIfEmpty(&s.TranscriptPath, str(ev.Data, "transcript_path"))

	case model.KindTurnStart:
		e.endWaiting(s, ev.TS)
		s.Status = StatusRunning
		s.Turns++
		s.NowDoing = "Thinking…"
		setIfEmpty(&s.Title, TitleFromPrompt(str(ev.Data, "prompt")))
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
		s.InputTokens += int64(num(ev.Data, "input_tokens"))
		s.OutputTokens += int64(num(ev.Data, "output_tokens"))
		s.CacheReadTokens += int64(num(ev.Data, "cache_read_tokens"))
		s.CacheWriteTokens += int64(num(ev.Data, "cache_write_tokens"))
		if c, ok := ev.Data["cost_usd"].(float64); ok {
			s.CostUSD += c
		}
		setIfEmpty(&s.Model, str(ev.Data, "model"))

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
		Depth: parent.Depth + 1, CWD: parent.CWD, Status: StatusRunning, StartedAt: ev.TS, LastEventAt: ev.TS,
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
