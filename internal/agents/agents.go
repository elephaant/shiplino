// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

// Package agents lists the agents Shiplino connects to through hook
// config files, for setup, uninstall, doctor and the settings page.
package agents

import (
	"context"
	"errors"
	"strings"

	"github.com/elephaant/shiplino/pkg/adapters/claudecode"
	"github.com/elephaant/shiplino/pkg/adapters/codex"
	"github.com/elephaant/shiplino/pkg/adapters/cursor"
	"github.com/elephaant/shiplino/pkg/adapters/windsurf"
)

// Hooks is one agent whose hooks Shiplino manages.
type Hooks struct {
	Name, ID string
	// Detect returns whether the agent is on this machine, its version and
	// the user-level hook config file.
	Detect    func(ctx context.Context, home string) (found bool, version, path string)
	Install   func(path, bin, version, backupDir string) (changed bool, events int, err error)
	Uninstall func(path, backupDir string) (changed bool, err error)
	Installed func(path string) (bool, string, error)
	Note      string // shown after a fresh install
}

// errUnparseable is shared by every hook-file adapter.
var errUnparseable = claudecode.ErrUnparseable

// All are the agents Shiplino can connect.
var All = []Hooks{
	{
		Name: "Claude Code", ID: claudecode.Name,
		Detect: func(ctx context.Context, home string) (bool, string, string) {
			d := claudecode.Detect(ctx, home)
			return d.Installed, d.Version, d.SettingsPath
		},
		Install: func(path, bin, version, backup string) (bool, int, error) {
			r, err := claudecode.Install(path, bin, version, backup)
			return r.Changed, len(r.Events), err
		},
		Uninstall: func(path, backup string) (bool, error) {
			r, err := claudecode.Uninstall(path, backup)
			return r.Changed, err
		},
		Installed: claudecode.Installed,
	},
	{
		Name: "Codex", ID: codex.Name,
		Detect: func(ctx context.Context, home string) (bool, string, string) {
			d := codex.Detect(ctx, home)
			return d.Installed, d.Version, d.HooksPath
		},
		Install: func(path, bin, _, backup string) (bool, int, error) {
			r, err := codex.Install(path, bin, backup)
			return r.Changed, len(r.Events), err
		},
		Uninstall: func(path, backup string) (bool, error) {
			r, err := codex.Uninstall(path, backup)
			return r.Changed, err
		},
		Installed: codex.Installed,
		Note:      codex.TrustNote,
	},
	{
		Name: "Cursor", ID: cursor.Name,
		Detect: func(ctx context.Context, home string) (bool, string, string) {
			d := cursor.Detect(ctx, home)
			return d.Installed, d.Version, d.HooksPath
		},
		Install: func(path, bin, _, backup string) (bool, int, error) {
			r, err := cursor.Install(path, bin, backup)
			return r.Changed, len(r.Events), err
		},
		Uninstall: func(path, backup string) (bool, error) {
			r, err := cursor.Uninstall(path, backup)
			return r.Changed, err
		},
		Installed: cursor.Installed,
	},
	{
		Name: "Windsurf", ID: windsurf.Name,
		Detect: func(ctx context.Context, home string) (bool, string, string) {
			d := windsurf.Detect(ctx, home)
			return d.Installed, d.Version, d.HooksPath
		},
		Install: func(path, bin, _, backup string) (bool, int, error) {
			r, err := windsurf.Install(path, bin, backup)
			return r.Changed, len(r.Events), err
		},
		Uninstall: func(path, backup string) (bool, error) {
			r, err := windsurf.Uninstall(path, backup)
			return r.Changed, err
		},
		Installed: windsurf.Installed,
		Note:      windsurf.Note,
	},
}

// IsUnparseable reports whether err means the config file isn't plain
// JSON, so Shiplino left it untouched.
func IsUnparseable(err error) bool { return errors.Is(err, errUnparseable) }

// Status is an agent's connection state on this machine.
type Status struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Found     bool   `json:"found"`
	Version   string `json:"version,omitempty"`
	HooksPath string `json:"hooks_path,omitempty"`
	Connected bool   `json:"connected"`         // our hook is installed
	Current   bool   `json:"current"`           // and points at bin
	Problem   string `json:"problem,omitempty"` // e.g. the config isn't plain JSON
}

// Statuses checks every agent's hook config. bin is the installed
// Shiplino binary the hooks should run.
func Statuses(ctx context.Context, home, bin string) []Status {
	out := make([]Status, 0, len(All))
	for _, a := range All {
		st := Status{ID: a.ID, Name: a.Name}
		st.Found, st.Version, st.HooksPath = a.Detect(ctx, home)
		if st.Found {
			ok, cmd, err := a.Installed(st.HooksPath)
			switch {
			case IsUnparseable(err):
				st.Problem = "config isn't plain JSON (comments?); Shiplino won't edit it"
			case err != nil:
				st.Problem = err.Error()
			}
			st.Connected = ok
			st.Current = ok && strings.Contains(cmd, bin)
		}
		out = append(out, st)
	}
	return out
}
