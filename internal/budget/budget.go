// Package budget watches spend against the user's budgets and sends the
// daily digest.
//
// Spend is counted the way Insights counts it: a top-level session's best
// cost (its subagents included) on the local day it started. Each budget
// notifies once at 80% and once at 100% per period; what was sent is
// remembered across restarts.
package budget

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/elephaant/shiplino/internal/notify"
	"github.com/elephaant/shiplino/pkg/engine"
)

// Config is what the user set. Zero amounts mean no budget.
type Config struct {
	DailyUSD   float64
	MonthlyUSD float64
	// Projects are daily budgets per project, keyed by project id or
	// short name ("api" matches github.com/acme/api).
	Projects map[string]float64
	// Digest is the local time of the daily summary ("18:00"); "" is off.
	Digest string
}

// Enabled reports whether there's anything to watch.
func (c Config) Enabled() bool {
	return c.DailyUSD > 0 || c.MonthlyUSD > 0 || len(c.Projects) > 0 || c.Digest != ""
}

// Store is what the watcher needs from the database.
type Store interface {
	SessionsActiveSince(ctx context.Context, t time.Time) ([]*engine.Session, error)
	Meta(ctx context.Context, key string) (string, error)
	SetMeta(ctx context.Context, key, value string) error
}

// Watcher checks budgets and sends the digest.
type Watcher struct {
	cfg  Config
	st   Store
	send func(context.Context, notify.Note) error
	now  func() time.Time
}

// New returns a watcher; send nil uses desktop notifications.
func New(cfg Config, st Store, send func(context.Context, notify.Note) error) *Watcher {
	if send == nil {
		send = notify.Send
	}
	return &Watcher{cfg: cfg, st: st, send: send, now: time.Now}
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

// Spend is the current state of one budget.
type Spend struct {
	Scope    string  `json:"scope"` // "today", "month" or "project:<key>"
	Label    string  `json:"label"`
	SpentUSD float64 `json:"spent_usd"`
	LimitUSD float64 `json:"limit_usd"`
}

// Status returns spend against every configured budget.
func (w *Watcher) Status(ctx context.Context) ([]Spend, error) {
	now := w.now()
	day := startOfDay(now)
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	from := day
	if w.cfg.MonthlyUSD > 0 && month.Before(from) {
		from = month
	}
	list, err := w.st.SessionsActiveSince(ctx, from)
	if err != nil {
		return nil, err
	}
	var today, thisMonth float64
	byProject := map[string]float64{}
	for _, s := range list {
		if s.ParentID != "" || s.StartedAt.Before(from) {
			continue
		}
		if !s.StartedAt.Before(month) {
			thisMonth += s.BestCostUSD
		}
		if !s.StartedAt.Before(day) {
			today += s.BestCostUSD
			byProject[s.ProjectID] += s.BestCostUSD
		}
	}
	var out []Spend
	if w.cfg.DailyUSD > 0 {
		out = append(out, Spend{Scope: "today", Label: "Today", SpentUSD: today, LimitUSD: w.cfg.DailyUSD})
	}
	if w.cfg.MonthlyUSD > 0 {
		out = append(out, Spend{Scope: "month", Label: "This month", SpentUSD: thisMonth, LimitUSD: w.cfg.MonthlyUSD})
	}
	keys := make([]string, 0, len(w.cfg.Projects))
	for k := range w.cfg.Projects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		var spent float64
		for id, usd := range byProject {
			if matches(k, id) {
				spent += usd
			}
		}
		out = append(out, Spend{Scope: "project:" + k, Label: k + " today", SpentUSD: spent, LimitUSD: w.cfg.Projects[k]})
	}
	return out, nil
}

// Check sends any budget alerts and the digest that are due.
func (w *Watcher) Check(ctx context.Context) error {
	spends, err := w.Status(ctx)
	if err != nil {
		return err
	}
	now := w.now()
	for _, s := range spends {
		period := now.Format("2006-01-02")
		if s.Scope == "month" {
			period = now.Format("2006-01")
		}
		for _, level := range []int{100, 80} {
			if s.SpentUSD < s.LimitUSD*float64(level)/100 {
				continue
			}
			key := fmt.Sprintf("budget:%s:%s:%d", s.Scope, period, level)
			if done, _ := w.st.Meta(ctx, key); done != "" {
				break // this level (and so the lower one) was already sent
			}
			title := fmt.Sprintf("%s: %d%% of budget used", s.Label, level)
			if level == 100 {
				title = s.Label + ": budget reached"
			}
			body := fmt.Sprintf("$%.2f of $%.2f spent at list prices.", s.SpentUSD, s.LimitUSD)
			if err := w.send(ctx, notify.Note{Title: title, Body: body}); err != nil {
				return err
			}
			// Reaching 100% first also marks 80% as sent.
			for _, l := range []int{80, 100} {
				if l <= level {
					_ = w.st.SetMeta(ctx, fmt.Sprintf("budget:%s:%s:%d", s.Scope, period, l), now.Format(time.RFC3339))
				}
			}
			break
		}
	}
	return w.digest(ctx, now)
}

// digest sends the day's summary once, at or after the configured time.
func (w *Watcher) digest(ctx context.Context, now time.Time) error {
	at, ok := parseClock(w.cfg.Digest)
	if !ok {
		return nil
	}
	due := startOfDay(now).Add(at)
	key := "digest:" + now.Format("2006-01-02")
	if now.Before(due) {
		return nil
	}
	if done, _ := w.st.Meta(ctx, key); done != "" {
		return nil
	}
	list, err := w.st.SessionsActiveSince(ctx, startOfDay(now))
	if err != nil {
		return err
	}
	var sessions, files, failed int
	var cost float64
	var waiting int64
	agents := map[string]bool{}
	for _, s := range list {
		if s.ParentID != "" || s.StartedAt.Before(startOfDay(now)) {
			continue
		}
		sessions++
		cost += s.BestCostUSD
		files += len(s.Files)
		waiting += s.WaitingMS
		agents[notify.AgentName(s.Agent)] = true
		if s.Status == engine.StatusFailed {
			failed++
		}
	}
	if sessions == 0 {
		_ = w.st.SetMeta(ctx, key, "empty")
		return nil // nothing happened: don't notify
	}
	names := make([]string, 0, len(agents))
	for a := range agents {
		names = append(names, a)
	}
	sort.Strings(names)
	body := fmt.Sprintf("%d session%s (%s), $%.2f, %d file%s changed", sessions, plural(sessions), strings.Join(names, ", "), cost, files, plural(files))
	if waiting > 0 {
		body += fmt.Sprintf(", %d min waiting on you", waiting/60000)
	}
	if failed > 0 {
		body += fmt.Sprintf(", %d failed", failed)
	}
	if err := w.send(ctx, notify.Note{Title: "Today with your agents", Body: body + "."}); err != nil {
		return err
	}
	return w.st.SetMeta(ctx, key, now.Format(time.RFC3339))
}

// matches tells whether a budget key names a project id: the whole id or
// its last path element ("api" for github.com/acme/api).
func matches(key, projectID string) bool {
	if key == projectID {
		return true
	}
	return projectID != "" && path.Base(strings.TrimSuffix(projectID, "/")) == key
}

// ParseClock reads "HH:MM" (24-hour).
func ParseClock(s string) (time.Duration, bool) { return parseClock(s) }

func parseClock(s string) (time.Duration, bool) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return 0, false
	}
	return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute, true
}

func startOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
