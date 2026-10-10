package daemon

import (
	"context"
	"github.com/elephaant/shiplino/internal/budget"
	"github.com/elephaant/shiplino/internal/integrations/github"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/elephaant/shiplino/internal/agents"
	"github.com/elephaant/shiplino/internal/config"
	"github.com/elephaant/shiplino/internal/notify"
	"github.com/elephaant/shiplino/internal/spool"
	cloudsync "github.com/elephaant/shiplino/internal/sync"
	"github.com/elephaant/shiplino/internal/update"
)

// admin backs the settings page (api.Admin).
type admin struct {
	d       *Daemon
	home    string
	cfg     config.Config
	version string
	port    int
	send    func(context.Context, notify.Note) error
	sync    *cloudsync.Uploader // nil in tests
	budget  *budget.Watcher     // nil when no budget or digest is set
	github  *github.Poller      // nil unless the GitHub integration is on
	agentMu sync.Mutex          // one agent config change at a time
}

// SettingsView is what GET /api/v1/settings returns.
type SettingsView struct {
	Version      string          `json:"version"`
	Home         string          `json:"home"`
	ConfigPath   string          `json:"config_path"`
	Port         int             `json:"port"`
	DataBytes    int64           `json:"data_bytes"`
	CaptureLevel string          `json:"capture_level"`
	ExtraRedact  int             `json:"extra_redaction_patterns"`
	Paused       bool            `json:"paused"`
	PausedUntil  time.Time       `json:"paused_until,omitzero"`
	Notify       NotifyView      `json:"notify"`
	Agents       []agents.Status `json:"agents"`
	Health       Health          `json:"health"`
	Sync         *cloudsync.View `json:"sync,omitempty"`
	Budget       BudgetView      `json:"budget"`
	GitHub       *github.Status  `json:"github,omitempty"`
	Update       UpdateView      `json:"update"`
}

// UpdateView is the update part of SettingsView, from the last check
// (update.json); it never calls the network itself.
type UpdateView struct {
	Check       bool      `json:"check"`
	AutoInstall bool      `json:"auto_install"`
	Channel     string    `json:"channel,omitempty"`
	Dev         bool      `json:"dev"` // a development build, never updated
	Available   string    `json:"available,omitempty"`
	URL         string    `json:"url,omitempty"`
	CheckedAt   time.Time `json:"checked_at,omitzero"`
	Error       string    `json:"error,omitempty"`
	// InstallError says why installing it automatically failed.
	InstallError string `json:"install_error,omitempty"`
}

func (a *admin) updateView() UpdateView {
	st := update.LoadState(a.home)
	v := UpdateView{Check: a.cfg.UpdateChecks(), AutoInstall: a.cfg.Update.AutoInstall, Channel: a.cfg.Update.Channel,
		Dev: update.IsDev(a.version), Available: st.Newer(a.version), CheckedAt: st.CheckedAt, Error: st.Error}
	if v.Available != "" {
		v.URL, v.InstallError = st.URL, st.InstallError
	}
	return v
}

// BudgetView is the budget part of SettingsView.
type BudgetView struct {
	Spends []budget.Spend `json:"spends"`
	Digest string         `json:"digest,omitempty"`
}

// NotifyView is the notification part of SettingsView.
type NotifyView struct {
	Enabled   bool  `json:"enabled"`
	Waiting   bool  `json:"waiting"`
	Finished  bool  `json:"finished"`
	Failed    bool  `json:"failed"`
	MinTurnMS int64 `json:"min_turn_ms"`
	// LimitPercent is the plan usage alert threshold (0 = off).
	LimitPercent float64 `json:"limit_percent"`
	Available    bool    `json:"available"`
	Via          string  `json:"via,omitempty"`
}

func (a *admin) Settings(ctx context.Context) any {
	userHome, _ := os.UserHomeDir()
	bin := a.bin()
	set, on := a.cfg.NotifySettings()
	ok, via := notify.Available()
	v := SettingsView{
		Version: a.version, Home: a.home, ConfigPath: config.Path(a.home), Port: a.port,
		CaptureLevel: string(a.cfg.Level()), ExtraRedact: len(a.cfg.Redaction.ExtraPatterns),
		Notify: NotifyView{Enabled: on, Waiting: set.Waiting, Finished: set.Finished, Failed: set.Failed,
			MinTurnMS: set.MinTurn.Milliseconds(), LimitPercent: a.cfg.LimitPercent(), Available: ok, Via: via},
		Agents: agents.Statuses(ctx, userHome, bin),
		Health: a.d.Health(),
		Update: a.updateView(),
	}
	// Paths are shown relative to the home directory, like a shell would.
	v.Home, v.ConfigPath = tilde(v.Home, userHome), tilde(v.ConfigPath, userHome)
	for i := range v.Agents {
		v.Agents[i].HooksPath = tilde(v.Agents[i].HooksPath, userHome)
	}
	if a.sync != nil {
		sv := a.sync.View(ctx)
		v.Sync = &sv
	}
	if a.github != nil {
		st := a.github.Status()
		v.GitHub = &st
	}
	v.Budget = BudgetView{Digest: a.cfg.Budget.Digest, Spends: []budget.Spend{}}
	if a.budget != nil {
		if spends, err := a.budget.Status(ctx); err == nil {
			v.Budget.Spends = spends
		}
	}
	v.Paused, v.PausedUntil = spool.PausedUntil(a.home, time.Now())
	for _, f := range []string{"shiplino.db", "shiplino.db-wal"} {
		if fi, err := os.Stat(filepath.Join(a.home, "data", f)); err == nil {
			v.DataBytes += fi.Size()
		}
	}
	return v
}

func (a *admin) Backfill(since time.Time) int { return a.d.Backfill(since) }

func (a *admin) Pause(until time.Time) error { return spool.Pause(a.home, until) }
func (a *admin) Resume() error               { return spool.Resume(a.home) }

func (a *admin) TestNotification(ctx context.Context) error {
	return a.send(ctx, notify.Note{Title: "Shiplino notifications work", Body: "You'll hear from Shiplino when an agent is waiting on you, finishes a long turn or fails."})
}

func tilde(path, home string) string {
	if home == "" {
		return path
	}
	if rel, err := filepath.Rel(home, path); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.Join("~", rel)
	}
	return path
}
