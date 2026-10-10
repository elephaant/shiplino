package limits

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/elephaant/shiplino/internal/notify"
	"github.com/elephaant/shiplino/internal/store"
	"github.com/elephaant/shiplino/pkg/adapters"
	"github.com/elephaant/shiplino/pkg/model"
)

var ctx = context.Background()

// now is 14:30 UTC on a fixed day (estimated windows start on a UTC hour).
var now = time.Date(2026, 10, 9, 14, 30, 0, 0, time.UTC)

func open(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "shiplino.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

var seq int

func put(t *testing.T, st *store.Store, evs ...model.Event) {
	t.Helper()
	tx, err := st.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if _, err := tx.InsertEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func ev(agent string, kind model.Kind, ts time.Time, data map[string]any) model.Event {
	seq++
	sid := agent + ":s1"
	return model.Event{ID: model.NewULID(ts), V: 1, TS: ts, Kind: kind, Agent: model.Agent{Name: agent},
		Collector: model.CollectorTranscript, SessionID: sid, ActorID: sid, Data: data, DedupKey: fmt.Sprint("k", seq)}
}

func limit(agent string, ts time.Time, minutes int64, used float64, resets time.Time) model.Event {
	return ev(agent, model.KindLimit, ts, adapters.LimitData(minutes, "", used, resets, "plus"))
}

func usage(agent string, ts time.Time, tokens int64, cost float64) model.Event {
	return ev(agent, model.KindUsage, ts, map[string]any{"input_tokens": tokens / 2, "output_tokens": tokens - tokens/2, "cost_usd": cost})
}

func watcher(st *store.Store, cfg Config, sent *[]notify.Note) *Watcher {
	w := New(cfg, st, func(_ context.Context, n notify.Note) error {
		*sent = append(*sent, n)
		return nil
	})
	w.now = func() time.Time { return now }
	return w
}

func TestReportedWindowsWin(t *testing.T) {
	st := open(t)
	put(t, st,
		limit("codex", now.Add(-time.Hour), 300, 40, now.Add(time.Hour)),
		limit("codex", now.Add(-time.Minute), 300, 62, now.Add(time.Hour)), // newest wins
		limit("codex", now.Add(-time.Minute), 10080, 18.5, now.Add(72*time.Hour)),
		limit("codex", now.Add(-6*time.Hour), 60, 90, now.Add(-5*time.Hour)), // reset: dropped
		usage("codex", now.Add(-time.Minute), 1000, 0.01),
	)
	var sent []notify.Note
	s, err := watcher(st, Config{}, &sent).Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Windows) != 2 || s.Plans["codex"] != PlanSubscription {
		t.Fatalf("status: %+v", s)
	}
	h5, wk := s.Windows[0], s.Windows[1]
	if h5.Window != "5h" || *h5.UsedPercent != 62 || h5.Source != "reported" || !h5.ResetsAt.Equal(now.Add(time.Hour).Truncate(time.Second)) || h5.Plan != "plus" {
		t.Fatalf("5h: %+v", h5)
	}
	if wk.Window != "7d" || *wk.UsedPercent != 18.5 || wk.Tokens != 0 {
		t.Fatalf("7d: %+v", wk)
	}
}

func TestEstimatesForPlanAgentsWithoutPercentages(t *testing.T) {
	st := open(t)
	put(t, st,
		// An earlier window (09:00-14:00) ended; the current one starts at
		// the hour of the next response (14:00).
		usage("claude-code", now.Add(-5*time.Hour), 500, 1),    // 09:30
		usage("claude-code", now.Add(-20*time.Minute), 300, 2), // 14:10
		usage("claude-code", now.Add(-5*time.Minute), 200, 3),  // 14:25
		usage("claude-code", now.Add(-8*24*time.Hour), 999, 9), // older than a week
		usage("gemini-cli", now.Add(-time.Minute), 100, 1),     // unknown plan: no estimate
	)
	var sent []notify.Note
	s, err := watcher(st, Config{Plans: map[string]string{"claude-code": "plan"}}, &sent).Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Windows) != 2 {
		t.Fatalf("windows: %+v", s.Windows)
	}
	h5, wk := s.Windows[0], s.Windows[1]
	if h5.Source != "estimate" || h5.UsedPercent != nil || h5.Tokens != 500 || h5.CostUSD != 5 ||
		!h5.ResetsAt.Equal(now.Truncate(time.Hour).Add(5*time.Hour)) {
		t.Fatalf("5h estimate: %+v", h5)
	}
	if wk.Source != "estimate" || wk.Tokens != 1000 || !wk.ResetsAt.IsZero() {
		t.Fatalf("7d estimate: %+v", wk)
	}

	// A refusal at the 5-hour limit is the agent's word: it replaces the
	// 5-hour estimate, and the agent counts as on a plan without config.
	put(t, st, ev("claude-code", model.KindLimit, now.Add(-time.Minute),
		map[string]any{"limit_window": "5h", "window_minutes": 300, "used_percent": 100, "limit_reached": true,
			"resets_at": now.Add(time.Hour).UTC().Format(time.RFC3339), "limit_source": "reported"}))
	s, _ = watcher(st, Config{}, &sent).Status(ctx)
	if len(s.Windows) != 2 || s.Windows[0].Source != "reported" || !s.Windows[0].Reached || s.Windows[1].Source != "estimate" {
		t.Fatalf("after refusal: %+v", s.Windows)
	}

	// Pay-per-token agents have no windows.
	s, _ = watcher(st, Config{Plans: map[string]string{"claude-code": "api"}}, &sent).Status(ctx)
	if len(s.Windows) != 0 || s.Plans["claude-code"] != PlanAPI {
		t.Fatalf("api: %+v", s)
	}
}

func TestIdleFiveHourEstimate(t *testing.T) {
	st := open(t)
	put(t, st, usage("claude-code", now.Add(-6*time.Hour), 100, 1))
	var sent []notify.Note
	s, _ := watcher(st, Config{Plans: map[string]string{"claude-code": "plan"}}, &sent).Status(ctx)
	if h5 := s.Windows[0]; h5.Tokens != 0 || !h5.ResetsAt.IsZero() {
		t.Fatalf("idle: %+v", h5)
	}
}

func TestNotifyOncePerWindow(t *testing.T) {
	st := open(t)
	put(t, st,
		limit("codex", now.Add(-2*time.Minute), 300, 79, now.Add(time.Hour)),
		limit("codex", now.Add(-time.Minute), 10080, 50, now.Add(48*time.Hour)),
	)
	var sent []notify.Note
	w := watcher(st, Config{NotifyPercent: 80}, &sent)
	if err := w.Check(ctx); err != nil || len(sent) != 0 {
		t.Fatalf("below threshold: %v %v", sent, err)
	}
	put(t, st, limit("codex", now.Add(-time.Minute), 300, 82, now.Add(time.Hour)))
	w.Check(ctx)
	put(t, st, limit("codex", now, 300, 91, now.Add(time.Hour+time.Minute))) // same window, reset moved a little
	w.Check(ctx)
	if len(sent) != 1 || !strings.Contains(sent[0].Title, "Codex: 82% of 5-hour limit used") || !strings.Contains(sent[0].Body, "Resets at") {
		t.Fatalf("sent: %+v", sent)
	}
	// The next window notifies again.
	now2 := now.Add(2 * time.Hour)
	put(t, st, limit("codex", now2, 300, 100, now2.Add(3*time.Hour)))
	w.now = func() time.Time { return now2 }
	w.Check(ctx)
	if len(sent) != 2 || !strings.Contains(sent[1].Title, "5-hour limit reached") {
		t.Fatalf("next window: %+v", sent)
	}
	// Off means off.
	sent = nil
	if err := watcher(st, Config{}, &sent).Check(ctx); err != nil || len(sent) != 0 {
		t.Fatalf("off: %v %v", sent, err)
	}
}

func TestEstimatesNeverNotify(t *testing.T) {
	st := open(t)
	put(t, st, usage("claude-code", now.Add(-time.Minute), 1e9, 100))
	var sent []notify.Note
	if err := watcher(st, Config{NotifyPercent: 1, Plans: map[string]string{"claude-code": "plan"}}, &sent).Check(ctx); err != nil || len(sent) != 0 {
		t.Fatalf("estimate notified: %v %v", sent, err)
	}
}
