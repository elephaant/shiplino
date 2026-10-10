// Package limits shows how close each agent is to its plan's usage windows
// (5-hour, weekly) and when they reset, and notifies at a threshold.
//
// The agent's own numbers win: Codex records the used percentage and reset
// time of each window in its rollouts, and Claude Code records when a
// request was refused at a limit. Where an agent on a plan reports no
// percentage, Shiplino sums the tokens of the current window instead and
// marks it as an estimate. Plan quotas aren't published, so an estimate
// has no percentage and is never notified.
package limits

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/elephaant/shiplino/internal/notify"
	"github.com/elephaant/shiplino/internal/store"
	"github.com/elephaant/shiplino/pkg/model"
)

// Plan values in Config.Plans.
const (
	PlanSubscription = "plan" // a flat-rate plan with usage windows
	PlanAPI          = "api"  // pay per token: no windows
)

// Config is what the user set.
type Config struct {
	// NotifyPercent notifies once per window when an agent reports at
	// least this much used (0 = off).
	NotifyPercent float64
	// Plans says, per agent, whether it runs on a plan or pays per token.
	// Agents not listed are on a plan once they report a limit.
	Plans map[string]string
}

// Store is what the watcher needs from the database.
type Store interface {
	LatestLimits(ctx context.Context, since time.Time) ([]model.Event, error)
	UsageSince(ctx context.Context, since time.Time) ([]store.UsageRow, error)
	Meta(ctx context.Context, key string) (string, error)
	SetMeta(ctx context.Context, key, value string) error
}

// Window is the state of one usage window of one agent.
type Window struct {
	Agent         string   `json:"agent"`
	Window        string   `json:"window"` // "5h", "7d", …
	WindowMinutes int64    `json:"window_minutes"`
	LimitID       string   `json:"limit_id,omitempty"`
	UsedPercent   *float64 `json:"used_percent,omitempty"` // nil: unknown (estimates)
	Reached       bool     `json:"reached,omitempty"`
	// ResetsAt is when the window resets; zero when unknown (a rolling
	// estimate).
	ResetsAt time.Time `json:"resets_at,omitzero"`
	Plan     string    `json:"plan,omitempty"` // as the agent names it
	// Source is "reported" (the agent's numbers) or "estimate" (Shiplino
	// summed the tokens of the window; no quota is known).
	Source string `json:"source"`
	// Tokens and CostUSD (API-equivalent) are set on estimates.
	Tokens    int64     `json:"tokens,omitempty"`
	CostUSD   float64   `json:"cost_usd,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitzero"`
}

// Status is what GET /api/v1/limits returns.
type Status struct {
	Windows []Window `json:"windows"`
	// Plans is "plan" or "api" per agent, as set or detected; agents
	// not listed are unknown.
	Plans map[string]string `json:"plans"`
}

// Standard windows estimated when an agent reports no percentages.
const (
	fiveHours = 300
	week      = 10080
)

// detectFor is how far back a reported limit marks an agent as on a plan.
const detectFor = 30 * 24 * time.Hour

// Watcher computes limit states and sends alerts.
type Watcher struct {
	cfg  Config
	st   Store
	send func(context.Context, notify.Note) error
	now  func() time.Time

	// Usage rows for estimates are read at most every usageTTL: the
	// page and the alert check both ask, and a week is many rows.
	mu       sync.Mutex
	rows     []store.UsageRow
	rowsAt   time.Time // when read (wall clock)
	rowsFrom time.Time // the start they cover
}

const usageTTL = 30 * time.Second

// New returns a watcher; send nil uses desktop notifications.
func New(cfg Config, st Store, send func(context.Context, notify.Note) error) *Watcher {
	if send == nil {
		send = notify.Send
	}
	return &Watcher{cfg: cfg, st: st, send: send, now: time.Now}
}

// Status returns every agent's current windows.
func (w *Watcher) Status(ctx context.Context) (Status, error) {
	now := w.now()
	out := Status{Windows: []Window{}, Plans: map[string]string{}}
	for a, p := range w.cfg.Plans {
		out.Plans[a] = p
	}
	evs, err := w.st.LatestLimits(ctx, now.Add(-detectFor))
	if err != nil {
		return out, err
	}
	percents := map[string]bool{} // agents that report percentages
	active := map[string]bool{}   // agent/minutes with a current reported window
	for _, e := range evs {
		a := e.Agent.Name
		if _, set := out.Plans[a]; !set {
			out.Plans[a] = PlanSubscription
		}
		win := fromEvent(e)
		if win.UsedPercent != nil && !win.Reached {
			percents[a] = true
		}
		// A window is dropped once it resets, as the agents do; one
		// without a reset time counts for its length.
		if !win.ResetsAt.IsZero() && !win.ResetsAt.After(now) ||
			win.ResetsAt.IsZero() && now.Sub(win.UpdatedAt) > time.Duration(win.WindowMinutes)*time.Minute {
			continue
		}
		if out.Plans[a] == PlanAPI {
			continue
		}
		active[fmt.Sprintf("%s/%d", a, win.WindowMinutes)] = true
		out.Windows = append(out.Windows, win)
	}

	// Estimates for agents on a plan that report no percentages.
	var need []string
	for a, p := range out.Plans {
		if p == PlanSubscription && !percents[a] {
			need = append(need, a)
		}
	}
	if len(need) > 0 {
		rows, err := w.usage(ctx, now.Add(-week*time.Minute))
		if err != nil {
			return out, err
		}
		for _, a := range need {
			if !active[fmt.Sprintf("%s/%d", a, fiveHours)] {
				out.Windows = append(out.Windows, block(a, rows, now))
			}
			if !active[fmt.Sprintf("%s/%d", a, week)] {
				out.Windows = append(out.Windows, rolling(a, rows, now))
			}
		}
	}
	sort.Slice(out.Windows, func(i, j int) bool {
		x, y := out.Windows[i], out.Windows[j]
		if x.Agent != y.Agent {
			return x.Agent < y.Agent
		}
		if x.WindowMinutes != y.WindowMinutes {
			return x.WindowMinutes < y.WindowMinutes
		}
		return x.LimitID < y.LimitID
	})
	return out, nil
}

// usage returns usage rows since from, cached for usageTTL.
func (w *Watcher) usage(ctx context.Context, from time.Time) ([]store.UsageRow, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.rowsAt.IsZero() && time.Since(w.rowsAt) < usageTTL && !from.Before(w.rowsFrom) {
		return w.rows, nil
	}
	rows, err := w.st.UsageSince(ctx, from)
	if err != nil {
		return nil, err
	}
	w.rows, w.rowsAt, w.rowsFrom = rows, time.Now(), from
	return rows, nil
}

func fromEvent(e model.Event) Window {
	d := e.Data
	win := Window{Agent: e.Agent.Name, Source: "reported", UpdatedAt: e.TS}
	win.Window, _ = d["limit_window"].(string)
	win.LimitID, _ = d["limit_id"].(string)
	win.Plan, _ = d["plan_type"].(string)
	win.Reached, _ = d["limit_reached"].(bool)
	if m, ok := d["window_minutes"].(float64); ok {
		win.WindowMinutes = int64(m)
	}
	if p, ok := d["used_percent"].(float64); ok {
		win.UsedPercent = &p
	}
	if s, ok := d["resets_at"].(string); ok {
		win.ResetsAt, _ = time.Parse(time.RFC3339, s)
	}
	return win
}

// block estimates the current 5-hour window the way usage tools for
// these plans commonly do: a window starts at the hour (UTC) of the first
// response after the previous one ended and lasts five hours. Outside a
// window (no recent use) the estimate is empty with no reset time.
func block(agent string, rows []store.UsageRow, now time.Time) Window {
	win := Window{Agent: agent, Window: "5h", WindowMinutes: fiveHours, Source: "estimate"}
	var start, end time.Time
	for _, r := range rows {
		if r.Agent != agent || r.TS.After(now) {
			continue
		}
		if end.IsZero() || !r.TS.Before(end) {
			start = r.TS.Truncate(time.Hour)
			end = start.Add(fiveHours * time.Minute)
			win.Tokens, win.CostUSD = 0, 0
		}
		win.Tokens += r.Tokens
		win.CostUSD += r.CostUSD
		win.UpdatedAt = r.TS
	}
	if end.IsZero() || !now.Before(end) {
		return Window{Agent: agent, Window: "5h", WindowMinutes: fiveHours, Source: "estimate"}
	}
	win.ResetsAt = end
	return win
}

// rolling sums the last seven days. The real weekly window starts at a
// time only the provider knows, so there's no reset time.
func rolling(agent string, rows []store.UsageRow, now time.Time) Window {
	win := Window{Agent: agent, Window: "7d", WindowMinutes: week, Source: "estimate"}
	from := now.Add(-week * time.Minute)
	for _, r := range rows {
		if r.Agent == agent && !r.TS.Before(from) && !r.TS.After(now) {
			win.Tokens += r.Tokens
			win.CostUSD += r.CostUSD
			win.UpdatedAt = r.TS
		}
	}
	return win
}

// Run checks once a minute until ctx is cancelled.
func (w *Watcher) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		_ = w.Check(ctx) // best effort: a failed check is retried next minute
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Check notifies, once per window, when an agent reports usage at or
// above the threshold. What was sent is remembered across restarts.
func (w *Watcher) Check(ctx context.Context) error {
	if w.cfg.NotifyPercent <= 0 {
		return nil
	}
	st, err := w.Status(ctx)
	if err != nil {
		return err
	}
	now := w.now()
	for _, win := range st.Windows {
		if win.Source != "reported" || win.UsedPercent == nil || *win.UsedPercent < w.cfg.NotifyPercent {
			continue
		}
		key := fmt.Sprintf("limit:%s:%s:%d", win.Agent, win.LimitID, win.WindowMinutes)
		if until, _ := w.st.Meta(ctx, key); until != "" {
			if t, err := time.Parse(time.RFC3339, until); err == nil && now.Before(t) {
				continue // already sent for this window
			}
		}
		resets := win.ResetsAt
		if resets.IsZero() {
			resets = now.Add(time.Duration(win.WindowMinutes) * time.Minute)
		}
		if err := w.send(ctx, Note(win)); err != nil {
			return err
		}
		if err := w.st.SetMeta(ctx, key, resets.UTC().Format(time.RFC3339)); err != nil {
			return err
		}
	}
	return nil
}

// Note is the notification for a window at or over the threshold.
func Note(win Window) notify.Note {
	name := notify.AgentName(win.Agent)
	if win.LimitID != "" {
		name += " (" + win.LimitID + ")"
	}
	title := fmt.Sprintf("%s: %.0f%% of %s limit used", name, *win.UsedPercent, Label(win.Window))
	if win.Reached || *win.UsedPercent >= 100 {
		title = fmt.Sprintf("%s: %s limit reached", name, Label(win.Window))
	}
	body := "The agent reported it."
	if !win.ResetsAt.IsZero() {
		body = "Resets at " + win.ResetsAt.Local().Format("Mon 15:04") + "."
	}
	alert := notify.Alert{Event: notify.EventLimit, Agent: win.Agent, Window: win.Window, Percent: *win.UsedPercent, ResetsAt: win.ResetsAt}
	if win.Reached && alert.Percent < 100 {
		alert.Percent = 100
	}
	return notify.Note{Title: title, Body: body, Alerts: []notify.Alert{alert}}
}

// Label names a window for people: "5-hour", "weekly".
func Label(window string) string {
	switch window {
	case "5h":
		return "5-hour"
	case "7d":
		return "weekly"
	case "1d":
		return "daily"
	}
	return window
}
