package daemon

import (
	"context"
	"errors"
	"github.com/elephaant/shiplino/internal/budget"
	"github.com/elephaant/shiplino/internal/integrations/github"
	"github.com/elephaant/shiplino/pkg/model"
	"log"
	"net"
	"os"
	"path/filepath"

	"github.com/elephaant/shiplino/internal/api"
	"github.com/elephaant/shiplino/internal/config"
	"github.com/elephaant/shiplino/internal/notify"
	"github.com/elephaant/shiplino/internal/spool"
	"github.com/elephaant/shiplino/internal/store"
	cloudsync "github.com/elephaant/shiplino/internal/sync"
	"github.com/elephaant/shiplino/pkg/engine"
	"github.com/elephaant/shiplino/pkg/redact"
)

// Main runs the daemon in the foreground until ctx is cancelled.
func Main(ctx context.Context, version string) error {
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
	ln, err := api.Listen(home, api.DefaultPort)
	if err != nil {
		return err
	}
	hub := api.NewHub()
	d.OnChange = hub.Publish

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go hub.Run(ctx)
	if set, on := cfg.NotifySettings(); on {
		n := notify.New(set, nil)
		d.OnChange = func(list []*engine.Session) {
			hub.Publish(list)
			n.Observe(list)
		}
		go n.Run(ctx)
	}
	// Opt-in cloud sync: idle unless enabled and signed in, on its own
	// goroutine reading committed rows, so it never slows recording.
	uploader := cloudsync.NewUploader(home, st, logger, version)
	go uploader.Run(ctx)
	var prs *github.Poller
	if cfg.Integrations.GitHub.Enabled {
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
	if bc := (budget.Config{DailyUSD: cfg.Budget.DailyUSD, MonthlyUSD: cfg.Budget.MonthlyUSD, Projects: cfg.Budget.Projects, Digest: cfg.Budget.Digest}); bc.Enabled() {
		budgets = budget.New(bc, st, nil)
		go budgets.Run(ctx)
	}
	apiErr := make(chan error, 1)
	go func() {
		srv := api.New(st, hub, token, version, logger)
		srv.Status = func() any { return d.Health() }
		srv.Admin = &admin{d: d, home: home, cfg: cfg, version: version, sync: uploader, budget: budgets, github: prs, port: ln.Addr().(*net.TCPAddr).Port, send: notify.Send}
		srv.Ingest = d
		srv.DevOrigin = os.Getenv("SHIPLINO_DEV_ORIGIN")
		srv.Level, srv.Redactor = level, redactor
		srv.UserHome, _ = os.UserHomeDir()
		apiErr <- srv.Serve(ctx, ln)
		cancel() // if the API dies, stop the daemon too
	}()

	logger.Printf("daemon started: http://localhost:%d (home %s, capture level %s)", ln.Addr().(*net.TCPAddr).Port, home, cfg.Level())
	err = errors.Join(d.Run(ctx), <-apiErr)
	s := d.Stats()
	logger.Printf("daemon stopped: %d lines, %d events, %d unknown, %d bad", s.Lines, s.Events, s.Unknown, s.Bad)
	return err
}
