// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/elephaant/shiplino/internal/config"
	"github.com/elephaant/shiplino/internal/notify"
)

func TestAdmin(t *testing.T) {
	home := withHome(t)
	e := newEnv(t)
	os.MkdirAll(filepath.Join(home, ".codex"), 0o700) // Codex is "installed", not connected
	var sent []notify.Note
	a := &admin{d: e.d, home: e.home, cfg: config.Config{}, version: "test", port: 4777,
		send: func(_ context.Context, n notify.Note) error { sent = append(sent, n); return nil }}

	v := a.Settings(ctx).(SettingsView)
	if v.CaptureLevel != "standard" || !v.Notify.Enabled || v.Notify.MinTurnMS != 30_000 || v.Paused {
		t.Fatalf("settings: %+v", v)
	}
	var codex bool
	for _, s := range v.Agents {
		if s.ID == "codex" {
			codex = s.Found && !s.Connected
		}
	}
	if !codex {
		t.Fatalf("agents: %+v", v.Agents)
	}
	a.Pause(time.Now().Add(time.Hour))
	if v := a.Settings(ctx).(SettingsView); !v.Paused || v.PausedUntil.IsZero() {
		t.Fatalf("paused: %+v", v)
	}
	a.Resume()
	if v := a.Settings(ctx).(SettingsView); v.Paused {
		t.Fatal("still paused")
	}
	if err := a.TestNotification(ctx); err != nil || len(sent) != 1 {
		t.Fatalf("notify: %v %v", err, sent)
	}
}
