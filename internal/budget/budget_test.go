// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

package budget

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/elephaant/shiplino/internal/notify"
	"github.com/elephaant/shiplino/pkg/engine"
)

type fakeStore struct {
	sessions []*engine.Session
	meta     map[string]string
}

func (f *fakeStore) SessionsActiveSince(_ context.Context, t time.Time) ([]*engine.Session, error) {
	var out []*engine.Session
	for _, s := range f.sessions {
		if !s.LastEventAt.Before(t) {
			out = append(out, s)
		}
	}
	return out, nil
}
func (f *fakeStore) Meta(_ context.Context, k string) (string, error) { return f.meta[k], nil }
func (f *fakeStore) SetMeta(_ context.Context, k, v string) error     { f.meta[k] = v; return nil }

var ctx = context.Background()

func setup(cfg Config, now time.Time, sessions ...*engine.Session) (*Watcher, *fakeStore, *[]notify.Note) {
	st := &fakeStore{sessions: sessions, meta: map[string]string{}}
	var sent []notify.Note
	w := New(cfg, st, func(_ context.Context, n notify.Note) error { sent = append(sent, n); return nil })
	w.now = func() time.Time { return now }
	return w, st, &sent
}

func sess(id, project string, started time.Time, cost float64) *engine.Session {
	return &engine.Session{ID: id, RootID: id, Agent: "claude-code", ProjectID: project, StartedAt: started, LastEventAt: started, BestCostUSD: cost, Files: []string{"a"}}
}

func TestDailyBudgetAlertsOncePerLevel(t *testing.T) {
	now := time.Date(2026, 10, 10, 15, 0, 0, 0, time.Local)
	yesterday := sess("y", "p", now.AddDate(0, 0, -1), 50) // doesn't count today
	a := sess("a", "github.com/acme/api", now.Add(-2*time.Hour), 8.5)
	w, st, sent := setup(Config{DailyUSD: 10}, now, yesterday, a)

	w.Check(ctx)
	if len(*sent) != 1 || !strings.Contains((*sent)[0].Title, "80%") || !strings.Contains((*sent)[0].Body, "$8.50 of $10.00") {
		t.Fatalf("80%%: %+v", *sent)
	}
	w.Check(ctx)
	if len(*sent) != 1 {
		t.Fatalf("80%% sent twice: %+v", *sent)
	}
	st.sessions = append(st.sessions, sess("b", "github.com/acme/api", now.Add(-time.Hour), 2))
	w.Check(ctx)
	if len(*sent) != 2 || !strings.Contains((*sent)[1].Title, "budget reached") {
		t.Fatalf("100%%: %+v", *sent)
	}
	w.Check(ctx)
	if len(*sent) != 2 {
		t.Fatalf("100%% sent twice: %+v", *sent)
	}
}

func TestJumpStraightTo100(t *testing.T) {
	now := time.Date(2026, 10, 10, 15, 0, 0, 0, time.Local)
	w, _, sent := setup(Config{DailyUSD: 5}, now, sess("a", "p", now.Add(-time.Hour), 7))
	w.Check(ctx)
	w.Check(ctx)
	if len(*sent) != 1 || !strings.Contains((*sent)[0].Title, "budget reached") {
		t.Fatalf("one alert at 100%%, no separate 80%%: %+v", *sent)
	}
}

func TestMonthlyAndProjectBudgets(t *testing.T) {
	now := time.Date(2026, 10, 10, 15, 0, 0, 0, time.Local)
	w, _, sent := setup(Config{MonthlyUSD: 100, Projects: map[string]float64{"api": 5, "github.com/acme/web": 50}}, now,
		sess("old", "github.com/acme/api", time.Date(2026, 10, 2, 9, 0, 0, 0, time.Local), 90), // this month, not today
		sess("a", "github.com/acme/api", now.Add(-time.Hour), 6),
		sess("w", "github.com/acme/web", now.Add(-time.Hour), 1),
		sess("sub", "github.com/acme/api", now.Add(-time.Hour), 999)) // subagent below
	(*w).st.(*fakeStore).sessions[3].ParentID = "a"
	spends, _ := w.Status(ctx)
	if len(spends) != 3 || spends[0].SpentUSD != 97 || spends[1].Scope != "project:api" || spends[1].SpentUSD != 6 || spends[2].SpentUSD != 1 {
		t.Fatalf("status: %+v", spends)
	}
	w.Check(ctx)
	titles := []string{}
	for _, n := range *sent {
		titles = append(titles, n.Title)
	}
	if len(*sent) != 2 || !strings.Contains(strings.Join(titles, "|"), "This month: 80%") || !strings.Contains(strings.Join(titles, "|"), "api today: budget reached") {
		t.Fatalf("alerts: %v", titles)
	}
}

func TestDigest(t *testing.T) {
	day := time.Date(2026, 10, 10, 0, 0, 0, 0, time.Local)
	s := sess("a", "p", day.Add(9*time.Hour), 1.25)
	s.WaitingMS = 5 * 60000
	f := sess("b", "p", day.Add(10*time.Hour), 0)
	f.Status = engine.StatusFailed
	f.Agent = "codex"
	w, _, sent := setup(Config{Digest: "18:00"}, day.Add(17*time.Hour), s, f)
	w.Check(ctx)
	if len(*sent) != 0 {
		t.Fatalf("digest before 18:00: %+v", *sent)
	}
	w.now = func() time.Time { return day.Add(18*time.Hour + time.Minute) }
	w.Check(ctx)
	w.Check(ctx)
	if len(*sent) != 1 || (*sent)[0].Body != "2 sessions (Claude Code, Codex), $1.25, 2 files changed, 5 min waiting on you, 1 failed." {
		t.Fatalf("digest: %+v", *sent)
	}
	// A quiet day sends nothing.
	w2, _, sent2 := setup(Config{Digest: "18:00"}, day.AddDate(0, 0, 1).Add(19*time.Hour))
	w2.Check(ctx)
	if len(*sent2) != 0 {
		t.Fatalf("empty digest sent: %+v", *sent2)
	}
}

func TestParseClock(t *testing.T) {
	if d, ok := ParseClock("18:30"); !ok || d != 18*time.Hour+30*time.Minute {
		t.Fatal(d, ok)
	}
	for _, bad := range []string{"", "6pm", "25:00", "18"} {
		if _, ok := ParseClock(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}
