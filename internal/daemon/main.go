package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"

	"github.com/elephaant/shiplino/internal/api"
	"github.com/elephaant/shiplino/internal/budget"
	"github.com/elephaant/shiplino/internal/config"
	"github.com/elephaant/shiplino/internal/integrations/github"
	"github.com/elephaant/shiplino/internal/limits"
	"github.com/elephaant/shiplino/internal/notify"
	"github.com/elephaant/shiplino/internal/notify/push"
	"github.com/elephaant/shiplino/internal/spool"
	"github.com/elephaant/shiplino/internal/store"
	cloudsync "github.com/elephaant/shiplino/internal/sync"
	"github.com/elephaant/shiplino/internal/update"
	"github.com/elephaant/shiplino/pkg/engine"
	"github.com/elephaant/shiplino/pkg/model"
	"github.com/elephaant/shiplino/pkg/redact"
)

// Options change how Run sets up the daemon. The zero value is the
// normal background service.
type Options struct {
	// Demo runs a throwaway instance for `shiplino demo`: it listens on
	// any free port, starts no notifications, sync, integrations or git
	// watching, and the API reports demo mode so the web app says so.
	Demo bool
	// Git, if set, answers project detection instead of the git binary
	// (demo data lives in folders that don't exist).
	Git ProjectGit
	// User, if set, replaces the OS user name recorded on events.
	User string
	// Ready, if set, is called on its own goroutine once the API is
	// listening, with the daemon and the board's address.
	Ready func(d *Daemon, url string)
}

// Main runs the daemon in the foreground until ctx is cancelled.
func Main(ctx context.Context, version string) error {
	return Run(ctx, version, Options{})
}

// Run is Main with options.
func Run(ctx context.Context, version string, opts Options) error {
	home := spool.Home()
	if home == "" {
		return errors.New("cannot find the home directory (set SHIPLINO_HOME)")
	}
	logger := log.New(os.Stderr, "shiplino: ", log.LstdFlags)

	lock, err := AcquireLock(home)
	if err != nil {
		return err
	}
	defer lock.Release()

	st, err := store.Open(filepath.Join(home, "data", "shiplino.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	d, err := New(ctx, home, st, logger)
	if err != nil {
		return err
	}
	if opts.Git != nil {
		d.projects.git = opts.Git
	}
	if opts.User != "" {
		d.user = opts.User
	}
	if opts.Demo {
		d.git = nil // demo folders aren't repos; their commits are seeded
	}
	cfg, err := config.Load(home)
	if err != nil {
		return err
	}
	level, redactor := cfg.Level(), cfg.Redactor()
	d.SetPrivacy(level, redactor)
	// The hook strips content itself at minimal, so prompts never reach
	// the spool on disk; it checks for this marker (no config parsing).
	marker := filepath.Join(home, spool.MinimalMarker)
	if cfg.Level() == redact.Minimal {
		_ = os.WriteFile(marker, nil, 0o600)
	} else {
		_ = os.Remove(marker)
	}
	token, err := api.LoadToken(home)
	if err != nil {
		return err
	}
	base := api.DefaultPort
	if opts.Demo {
		base = 0 // any free port: never in the way of the real daemon
	}
	ln, err := api.Listen(home, base)
	if err != nil {
		return err
	}
	hub := api.NewHub()
	d.OnChange = hub.Publish

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go hub.Run(ctx)
	// Notifications: on the desktop unless turned off, and to the push
	// targets the user added (none by default; they apply without a
	// restart). Alerts carry metadata only; see internal/notify/push.
	port := ln.Addr().(*net.TCPAddr).Port
	pusher := push.New(home, fmt.Sprintf("http://localhost:%d", port), config.Path(home), func() (push.Settings, error) {
		c, err := config.Load(home)
		return c.PushSettings(), err
	}, logger)
	if !opts.Demo {
		go pusher.Run(ctx)
	}
	set, desktop := cfg.NotifySettings()
	send := func(ctx context.Context, n notify.Note) error {
		pusher.Push(n.Alerts...)
		if !desktop {
			return nil
		}
		return notify.Send(ctx, n)
	}
	if !opts.Demo { // the demo's synthetic sessions never notify
		n := notify.New(set, send)
		d.OnChange = func(list []*engine.Session) {
			hub.Publish(list)
			n.Observe(list)
		}
		go n.Run(ctx)
	}
	// Opt-in cloud sync: idle unless enabled and signed in, on its own
	// goroutine reading committed rows, so it never slows recording.
	var uploader *cloudsync.Uploader
	if !opts.Demo {
		uploader = cloudsync.NewUploader(home, st, logger, version)
		go uploader.Run(ctx)
	}
	// Opt-in update checks: they call api.github.com, so only with
	// [update] check or auto_install.
	var restarting atomic.Bool
	if cfg.UpdateChecks() && !update.IsDev(version) && !opts.Demo {
		go updateChecker(home, version, cfg, logger, func() { restarting.Store(true); cancel() }).Run(ctx)
	}
	var prs *github.Poller
	if cfg.Integrations.GitHub.Enabled && !opts.Demo {
		record := func(ctx context.Context, evs []model.Event) error {
			_, _, err := d.Ingest(ctx, evs)
			if errors.Is(err, api.ErrPaused) {
				return nil
			}
			return err
		}
		prs = github.NewPoller(st, record, hub.Publish, cfg.GitHubPoll())
		go prs.Run(ctx)
	}
	var budgets *budget.Watcher
	if bc := (budget.Config{DailyUSD: cfg.Budget.DailyUSD, MonthlyUSD: cfg.Budget.MonthlyUSD, Projects: cfg.Budget.Projects, Digest: cfg.Budget.Digest}); bc.Enabled() && !opts.Demo {
		budgets = budget.New(bc, st, send)
		go budgets.Run(ctx)
	}
	// Plan usage windows: always served, notified only when on.
	lw := limits.New(limits.Config{NotifyPercent: cfg.LimitPercent(), Plans: cfg.Limits.Plans}, st, send)
	if cfg.LimitPercent() > 0 && !opts.Demo {
		go lw.Run(ctx)
	}
	apiErr := make(chan error, 1)
	go func() {
		srv := api.New(st, hub, token, version, logger)
		srv.Status = func() any { return d.Health() }
		srv.Limits = func(ctx context.Context) (any, error) { return lw.Status(ctx) }
		srv.Admin = &admin{d: d, home: home, cfg: cfg, version: version, sync: uploader, budget: budgets, github: prs, port: port, send: notify.Send}
		srv.Ingest = d
		srv.DevOrigin = os.Getenv("SHIPLINO_DEV_ORIGIN")
		srv.Level, srv.Redactor = level, redactor
		srv.UserHome, _ = os.UserHomeDir()
		srv.Demo = opts.Demo
		apiErr <- srv.Serve(ctx, ln)
		cancel() // if the API dies, stop the daemon too
	}()

	url := fmt.Sprintf("http://localhost:%d", port)
	logger.Printf("daemon started: %s (home %s, capture level %s)", url, home, cfg.Level())
	if opts.Ready != nil {
		go opts.Ready(d, url)
	}
	err = errors.Join(d.Run(ctx), <-apiErr)
	s := d.Stats()
	logger.Printf("daemon stopped: %d lines, %d events, %d unknown, %d bad", s.Lines, s.Events, s.Unknown, s.Bad)
	if restarting.Load() {
		return errors.Join(err, update.ErrRestart)
	}
	return err
}

// updateChecker builds the daily check. With auto_install it also
// installs, but only when this daemon runs the binary the hooks use and
// no package manager owns it; restart is called after an install.
func updateChecker(home, version string, cfg config.Config, logger *log.Logger, restart func()) *update.Checker {
	src := update.Source{UserAgent: "shiplino/" + version}
	chk := &update.Checker{Home: home, Current: version, Channel: cfg.Update.Channel, Source: &src, Logf: logger.Printf}
	if !cfg.Update.AutoInstall {
		return chk
	}
	bin := filepath.Join(home, "bin", "shiplino")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	self, _ := os.Executable()
	rs, _ := filepath.EvalSymlinks(self)
	rb, _ := filepath.EvalSymlinks(bin)
	if mgr, cmd := update.Managed(self); mgr != "" {
		logger.Printf("auto_install is off for this install: it's managed by %s (%s)", mgr, cmd)
		return chk
	}
	if rs == "" || rs != rb {
		logger.Printf("auto_install is off for this daemon: it runs %s, not %s", self, bin)
		return chk
	}
	chk.Updater = &update.Updater{Source: src, Target: bin, Current: version, RequireSignature: cfg.Update.RequireSignature}
	chk.Installed = func(update.Result) { restart() }
	return chk
}
