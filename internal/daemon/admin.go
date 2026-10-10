// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/elephaant/shiplino/internal/agents"
	"github.com/elephaant/shiplino/internal/config"
	"github.com/elephaant/shiplino/internal/notify"
	"github.com/elephaant/shiplino/internal/spool"
)

// admin backs the settings page (api.Admin).
type admin struct {
	d       *Daemon
	home    string
	cfg     config.Config
	version string
	port    int
	send    func(context.Context, notify.Note) error
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
}

// NotifyView is the notification part of SettingsView.
type NotifyView struct {
	Enabled   bool   `json:"enabled"`
	Waiting   bool   `json:"waiting"`
	Finished  bool   `json:"finished"`
	Failed    bool   `json:"failed"`
	MinTurnMS int64  `json:"min_turn_ms"`
	Available bool   `json:"available"`
	Via       string `json:"via,omitempty"`
}

func (a *admin) Settings(ctx context.Context) any {
	userHome, _ := os.UserHomeDir()
	bin := filepath.Join(a.home, "bin", "shiplino")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	set, on := a.cfg.NotifySettings()
	ok, via := notify.Available()
	v := SettingsView{
		Version: a.version, Home: a.home, ConfigPath: config.Path(a.home), Port: a.port,
		CaptureLevel: string(a.cfg.Level()), ExtraRedact: len(a.cfg.Redaction.ExtraPatterns),
		Notify: NotifyView{Enabled: on, Waiting: set.Waiting, Finished: set.Finished, Failed: set.Failed,
			MinTurnMS: set.MinTurn.Milliseconds(), Available: ok, Via: via},
		Agents: agents.Statuses(ctx, userHome, bin),
		Health: a.d.Health(),
	}
	// Paths are shown relative to the home directory, like a shell would.
	v.Home, v.ConfigPath = tilde(v.Home, userHome), tilde(v.ConfigPath, userHome)
	for i := range v.Agents {
		v.Agents[i].HooksPath = tilde(v.Agents[i].HooksPath, userHome)
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
