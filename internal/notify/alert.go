package notify

import (
	"path"
	"strings"
	"time"

	"github.com/elephaant/shiplino/pkg/engine"
)

// Alert events.
const (
	EventWaiting = "waiting" // an agent waits on you
	EventDone    = "done"    // a long turn finished
	EventFailed  = "failed"  // a session failed
	EventBudget  = "budget"  // a budget reached 80% or 100%
	EventLimit   = "limit"   // an agent reported a plan usage window near its limit
	EventDigest  = "digest"  // the daily summary
	EventTest    = "test"    // `shiplino notify test`
)

// Alert is what a notification is about, as metadata only: ids, names,
// states, numbers and times. It is what phone and team targets
// (internal/notify/push) receive, so it has no field for content: no
// prompt, title, command, file or the agent's own notification text.
// Desktop notifications, which stay on this machine, use Note instead.
type Alert struct {
	Event     string
	At        time.Time
	SessionID string
	Agent     string // agent id, e.g. "claude-code"
	Project   string // short project name ("api" for github.com/acme/api)
	Branch    string
	Status    string // the session's status
	// Reason is why it waits: "permission", "question" or "idle".
	Reason     string
	DurationMS int64   // waiting: the session so far; done: the turn
	CostUSD    float64 // the session's cost (budget: spent; digest: the day's)

	// Budgets: Scope is "today", "month" or "project:<name>".
	Scope    string
	LimitUSD float64
	Percent  float64 // budget or plan window used, 0-100

	// Plan usage windows.
	Window   string // "5h", "7d", …
	ResetsAt time.Time

	// The daily digest.
	Sessions, Failed, FilesChanged int
	WaitingMS                      int64
	Agents                         []string // agent ids
}

// AlertFor is the alert for a session event (waiting, done, failed).
func AlertFor(event string, s engine.Session, now time.Time, turn time.Duration) Alert {
	a := Alert{Event: event, At: now, SessionID: s.ID, Agent: s.Agent, Project: ProjectName(s.ProjectID),
		Branch: s.Branch, Status: string(s.Status), CostUSD: s.BestCostUSD}
	switch {
	case event == EventDone:
		a.DurationMS = turn.Milliseconds()
	case !s.StartedAt.IsZero() && now.After(s.StartedAt):
		a.DurationMS = now.Sub(s.StartedAt).Milliseconds()
	}
	if s.Status == engine.StatusWaiting {
		a.Reason = s.WaitingReason
	}
	return a
}

// ProjectName is the short name of a project id: its last path element.
func ProjectName(id string) string {
	if id == "" {
		return ""
	}
	return path.Base(strings.TrimSuffix(id, "/"))
}

// WaitingTitle says who waits and why, e.g. "Claude Code needs your
// approval".
func WaitingTitle(agent, reason string) string {
	name := AgentName(agent)
	switch reason {
	case "permission":
		return name + " needs your approval"
	case "question":
		return name + " has a question"
	case "idle":
		return name + " is waiting for your next prompt"
	}
	return name + " needs you"
}
