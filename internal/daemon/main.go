// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package daemon

import (
	"context"
	"errors"
	"log"
	"net"
	"os"
	"path/filepath"

	"github.com/elephaant/shiplino/internal/api"
	"github.com/elephaant/shiplino/internal/config"
	"github.com/elephaant/shiplino/internal/notify"
	"github.com/elephaant/shiplino/internal/spool"
	"github.com/elephaant/shiplino/internal/store"
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
	d.SetPrivacy(cfg.Level(), cfg.Redactor())
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
	apiErr := make(chan error, 1)
	go func() {
		srv := api.New(st, hub, token, version, logger)
		srv.Status = func() any { return d.Health() }
		srv.Admin = &admin{d: d, home: home, cfg: cfg, version: version, port: ln.Addr().(*net.TCPAddr).Port, send: notify.Send}
		srv.DevOrigin = os.Getenv("SHIPLINO_DEV_ORIGIN")
		apiErr <- srv.Serve(ctx, ln)
		cancel() // if the API dies, stop the daemon too
	}()

	logger.Printf("daemon started: http://localhost:%d (home %s, capture level %s)", ln.Addr().(*net.TCPAddr).Port, home, cfg.Level())
	err = errors.Join(d.Run(ctx), <-apiErr)
	s := d.Stats()
	logger.Printf("daemon stopped: %d lines, %d events, %d unknown, %d bad", s.Lines, s.Events, s.Unknown, s.Bad)
	return err
}
