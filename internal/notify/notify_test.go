package notify

import (
	"strings"
	"testing"
	"time"

	"github.com/elephaant/shiplino/pkg/engine"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func setup(set Settings) (*Notifier, *clock) {
	c := &clock{t: time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)}
	n := New(set, nil)
	n.now = c.now
	return n, c
}

func sess(id string, st engine.Status, at time.Time) *engine.Session {
	return &engine.Session{ID: id, Agent: "claude-code", Status: st, Title: "Fix login", ProjectID: "github.com/acme/api", LastEventAt: at, NowDoing: "Approve: npm run db:migrate"}
}

func TestWaitingIsDebounced(t *testing.T) {
	n, c := setup(Defaults)
	n.Observe([]*engine.Session{sess("a", engine.StatusRunning, c.t)})
	n.Observe([]*engine.Session{sess("a", engine.StatusWaiting, c.t)})
	if got := n.Due(); len(got) != 0 {
		t.Fatalf("notified before WaitDelay: %v", got)
	}
	c.advance(time.Second)
	n.Observe([]*engine.Session{sess("a", engine.StatusRunning, c.t)}) // answered quickly
	c.advance(WaitDelay)
	if got := n.Due(); len(got) != 0 {
		t.Fatalf("notified for an answered prompt: %v", got)
	}

	n.Observe([]*engine.Session{sess("a", engine.StatusWaiting, c.t)})
	c.advance(WaitDelay)
	got := n.Due()
	if len(got) != 1 || got[0].Title != "Claude Code needs you" || got[0].Body != "api · Fix login\nApprove: npm run db:migrate" {
		t.Fatalf("waiting note: %+v", got)
	}
}

func TestFinishedOnlyForLongTurns(t *testing.T) {
	n, c := setup(Defaults)
	n.Observe([]*engine.Session{sess("a", engine.StatusRunning, c.t)})
	c.advance(10 * time.Second)
	n.Observe([]*engine.Session{sess("a", engine.StatusDone, c.t)})
	if got := n.Due(); len(got) != 0 {
		t.Fatalf("short turn notified: %v", got)
	}
	n.Observe([]*engine.Session{sess("a", engine.StatusRunning, c.t)})
	c.advance(20 * time.Second)
	n.Observe([]*engine.Session{sess("a", engine.StatusWaiting, c.t)}) // waiting is part of the turn
	c.advance(20 * time.Second)
	n.Observe([]*engine.Session{sess("a", engine.StatusRunning, c.t)})
	c.advance(5 * time.Second)
	s := sess("a", engine.StatusReview, c.t)
	s.Files = []string{"a.ts", "b.ts"}
	n.Observe([]*engine.Session{s})
	got := n.Due()
	if len(got) != 1 || got[0].Title != "Claude Code finished" || !strings.Contains(got[0].Body, "Finished in 45s · 2 files changed") {
		t.Fatalf("finished note: %+v", got)
	}
}

func TestFailedGroupedAndCooldown(t *testing.T) {
	n, c := setup(Defaults)
	for _, id := range []string{"a", "b", "c"} {
		n.Observe([]*engine.Session{sess(id, engine.StatusRunning, c.t)})
		n.Observe([]*engine.Session{sess(id, engine.StatusFailed, c.t)})
	}
	got := n.Due()
	if len(got) != 1 || got[0].Title != "3 agents failed" || strings.Count(got[0].Body, "• ") != 3 {
		t.Fatalf("grouped: %+v", got)
	}
	c.advance(5 * time.Second)
	n.Observe([]*engine.Session{sess("a", engine.StatusRunning, c.t)})
	n.Observe([]*engine.Session{sess("a", engine.StatusFailed, c.t)})
	if got := n.Due(); len(got) != 0 {
		t.Fatalf("cooldown ignored: %v", got)
	}
}

func TestQuietCases(t *testing.T) {
	n, c := setup(Settings{Waiting: false, Finished: true, Failed: true, MinTurn: time.Second})
	n.Observe([]*engine.Session{sess("a", engine.StatusWaiting, c.t)}) // disabled
	old := sess("b", engine.StatusFailed, c.t.Add(-time.Hour))         // backlog after a restart
	n.Observe([]*engine.Session{old})
	sub := sess("a/sub:x", engine.StatusFailed, c.t)
	sub.ParentID = "a"
	n.Observe([]*engine.Session{sub})                               // subagents roll up
	n.Observe([]*engine.Session{sess("d", engine.StatusDone, c.t)}) // first sight: no turn length known
	c.advance(WaitDelay)
	if got := n.Due(); len(got) != 0 {
		t.Fatalf("expected silence, got %+v", got)
	}
}

func TestWaitingSaysWhyAndCarriesAlerts(t *testing.T) {
	n, c := setup(Defaults)
	s := sess("a", engine.StatusWaiting, c.t)
	s.WaitingReason, s.Branch, s.StartedAt, s.BestCostUSD = "permission", "main", c.t.Add(-2*time.Minute), 0.5
	n.Observe([]*engine.Session{sess("a", engine.StatusRunning, c.t)})
	n.Observe([]*engine.Session{s})
	c.advance(WaitDelay)
	got := n.Due()
	if len(got) != 1 || got[0].Title != "Claude Code needs your approval" || len(got[0].Alerts) != 1 {
		t.Fatalf("note: %+v", got)
	}
	a := got[0].Alerts[0]
	if a.Event != EventWaiting || a.Reason != "permission" || a.Project != "api" || a.Branch != "main" || a.CostUSD != 0.5 ||
		a.DurationMS != (2*time.Minute+WaitDelay).Milliseconds() {
		t.Fatalf("alert: %+v", a)
	}
}
