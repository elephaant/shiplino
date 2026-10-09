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
	"github.com/elephaant/shiplino/internal/spool"
	"github.com/elephaant/shiplino/internal/store"
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
	apiErr := make(chan error, 1)
	go func() {
		apiErr <- api.New(st, hub, token, version, logger).Serve(ctx, ln)
		cancel() // if the API dies, stop the daemon too
	}()

	logger.Printf("daemon started: http://localhost:%d (home %s)", ln.Addr().(*net.TCPAddr).Port, home)
	err = errors.Join(d.Run(ctx), <-apiErr)
	s := d.Stats()
	logger.Printf("daemon stopped: %d lines, %d events, %d unknown, %d bad", s.Lines, s.Events, s.Unknown, s.Bad)
	return err
}
