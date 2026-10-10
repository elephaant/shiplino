// Package notify sends desktop notifications when an agent is waiting on
// you, finished a long turn, or failed.
//
// Rules (chosen to stay quiet unless it matters):
//   - waiting: sent only if the session is still waiting after WaitDelay,
//     so prompts you answer right away don't notify;
//   - finished: only for turns that ran at least MinTurn (like a shell's
//     "notify when a long command finishes");
//   - failed: always;
//   - several at once are grouped into one notification, and a session
//     isn't notified twice for the same thing within Cooldown.
package notify

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/elephaant/shiplino/pkg/engine"
)

// Note is one desktop notification.
type Note struct {
	Title string
	Body  string
}

// Settings choose what is notified.
type Settings struct {
	Waiting, Finished, Failed bool
	MinTurn                   time.Duration // shortest turn worth a "finished" note
}

// Defaults are used when the config doesn't say otherwise.
var Defaults = Settings{Waiting: true, Finished: true, Failed: true, MinTurn: 30 * time.Second}

const (
	// WaitDelay is how long a session must keep waiting before it notifies.
	WaitDelay = 3 * time.Second
	// Cooldown stops repeat notes for the same session and reason.
	Cooldown = 30 * time.Second
	// stale: events older than this (a backlog after a restart) never notify.
	stale = 2 * time.Minute
)

type reason int

const (
	needsYou reason = iota
	finished
	failed
)

type pending struct {
	id     string
	reason reason
	due    time.Time
	s      engine.Session
	turn   time.Duration
}

type track struct {
	status       engine.Status
	runningSince time.Time
	last         map[reason]time.Time
}

// Notifier turns session changes into notifications.
type Notifier struct {
	send func(context.Context, Note) error
	now  func() time.Time
	set  Settings

	mu      sync.Mutex
	state   map[string]*track
	pending []pending
}

// New returns a notifier that delivers with send (nil: the OS notifier).
func New(set Settings, send func(context.Context, Note) error) *Notifier {
	if send == nil {
		send = Send
	}
	return &Notifier{send: send, now: time.Now, set: set, state: map[string]*track{}}
}

// Observe records session changes (the daemon's OnChange). It never
// blocks on delivery.
func (n *Notifier) Observe(list []*engine.Session) {
	n.mu.Lock()
	defer n.mu.Unlock()
	now := n.now()
	for _, s := range list {
		if s.ParentID != "" {
			continue // subagents roll up into their session's card
		}
		t := n.state[s.ID]
		if t == nil {
			t = &track{last: map[reason]time.Time{}}
			n.state[s.ID] = t
		}
		prev := t.status
		t.status = s.Status
		if s.Status == prev || now.Sub(s.LastEventAt) > stale {
			if s.Status == engine.StatusRunning && t.runningSince.IsZero() {
				t.runningSince = s.LastEventAt
			}
			continue
		}
		switch s.Status {
		case engine.StatusRunning:
			if prev != engine.StatusWaiting {
				t.runningSince = now // a new turn; waiting is part of the same one
			}
		case engine.StatusWaiting:
			if n.set.Waiting {
				n.pending = append(n.pending, pending{id: s.ID, reason: needsYou, due: now.Add(WaitDelay), s: *s})
			}
		case engine.StatusDone, engine.StatusReview:
			turn := now.Sub(t.runningSince)
			if n.set.Finished && prev == engine.StatusRunning && !t.runningSince.IsZero() && turn >= n.set.MinTurn {
				n.pending = append(n.pending, pending{id: s.ID, reason: finished, due: now, s: *s, turn: turn})
			}
			t.runningSince = time.Time{}
		case engine.StatusFailed:
			if n.set.Failed {
				n.pending = append(n.pending, pending{id: s.ID, reason: failed, due: now, s: *s})
			}
			t.runningSince = time.Time{}
		}
	}
}

// Run delivers due notifications until ctx is cancelled.
func (n *Notifier) Run(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, note := range n.Due() {
				sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
				_ = n.send(sctx, note) // best effort: a missing notifier must not stop the daemon
				cancel()
			}
		}
	}
}

// Due returns the notifications to show now, grouped by reason, and
// drops pending ones that no longer apply.
func (n *Notifier) Due() []Note {
	n.mu.Lock()
	defer n.mu.Unlock()
	now := n.now()
	groups := map[reason][]pending{}
	keep := n.pending[:0]
	for _, p := range n.pending {
		if p.due.After(now) {
			keep = append(keep, p)
			continue
		}
		t := n.state[p.id]
		if p.reason == needsYou && (t == nil || t.status != engine.StatusWaiting) {
			continue // answered within WaitDelay
		}
		if last := t.last[p.reason]; !last.IsZero() && now.Sub(last) < Cooldown {
			continue
		}
		t.last[p.reason] = now
		groups[p.reason] = append(groups[p.reason], p)
	}
	n.pending = keep
	var out []Note
	for _, r := range []reason{needsYou, failed, finished} {
		if ps := groups[r]; len(ps) > 0 {
			out = append(out, compose(r, ps))
		}
	}
	return out
}

func compose(r reason, ps []pending) Note {
	if len(ps) == 1 {
		p := ps[0]
		s := p.s
		head := line(s)
		switch r {
		case needsYou:
			what := s.NowDoing
			if what == "" {
				what = "Waiting for your input"
			}
			return Note{Title: AgentName(s.Agent) + " needs you", Body: head + "\n" + what}
		case failed:
			body := head
			if s.NowDoing != "" {
				body += "\n" + s.NowDoing
			}
			return Note{Title: AgentName(s.Agent) + " failed", Body: body}
		default:
			extra := "Finished in " + shortDuration(p.turn)
			if n := len(s.Files); n > 0 {
				extra += fmt.Sprintf(" · %d file%s changed", n, plural(n))
			}
			return Note{Title: AgentName(s.Agent) + " finished", Body: head + "\n" + extra}
		}
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].s.LastEventAt.After(ps[j].s.LastEventAt) })
	title := map[reason]string{needsYou: "%d agents need you", failed: "%d agents failed", finished: "%d agents finished"}[r]
	var lines []string
	for i, p := range ps {
		if i == 4 {
			lines = append(lines, fmt.Sprintf("and %d more", len(ps)-4))
			break
		}
		lines = append(lines, "• "+line(p.s))
	}
	return Note{Title: fmt.Sprintf(title, len(ps)), Body: strings.Join(lines, "\n")}
}

// line is "project · title".
func line(s engine.Session) string {
	title := s.Title
	if title == "" {
		title = "Untitled session"
	}
	if r := []rune(title); len(r) > 80 {
		title = string(r[:79]) + "…"
	}
	if s.ProjectID == "" {
		return title
	}
	return path.Base(strings.TrimSuffix(s.ProjectID, "/")) + " · " + title
}

// AgentName is the display name of an agent id.
func AgentName(id string) string {
	switch id {
	case "claude-code":
		return "Claude Code"
	case "codex":
		return "Codex"
	case "cursor":
		return "Cursor"
	case "copilot-cli":
		return "Copilot CLI"
	case "opencode":
		return "OpenCode"
	}
	return id
}

func shortDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
