// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package cli

import (
	"context"
	"errors"

	"github.com/elephaant/shiplino/pkg/adapters/claudecode"
	"github.com/elephaant/shiplino/pkg/adapters/codex"
)

// agentHooks is one agent `setup`, `uninstall` and `doctor` manage.
type agentHooks struct {
	name, id string
	// detect returns whether the agent is on this machine, its version and
	// the user-level hook config file.
	detect    func(ctx context.Context, home string) (found bool, version, path string)
	install   func(path, bin, version, backupDir string) (changed bool, events int, err error)
	uninstall func(path, backupDir string) (changed bool, err error)
	installed func(path string) (bool, string, error)
	note      string // shown after a fresh install
}

// errUnparseable is shared by every hook-file adapter.
var errUnparseable = claudecode.ErrUnparseable

var agents = []agentHooks{
	{
		name: "Claude Code", id: claudecode.Name,
		detect: func(ctx context.Context, home string) (bool, string, string) {
			d := claudecode.Detect(ctx, home)
			return d.Installed, d.Version, d.SettingsPath
		},
		install: func(path, bin, version, backup string) (bool, int, error) {
			r, err := claudecode.Install(path, bin, version, backup)
			return r.Changed, len(r.Events), err
		},
		uninstall: func(path, backup string) (bool, error) {
			r, err := claudecode.Uninstall(path, backup)
			return r.Changed, err
		},
		installed: claudecode.Installed,
	},
	{
		name: "Codex", id: codex.Name,
		detect: func(ctx context.Context, home string) (bool, string, string) {
			d := codex.Detect(ctx, home)
			return d.Installed, d.Version, d.HooksPath
		},
		install: func(path, bin, _, backup string) (bool, int, error) {
			r, err := codex.Install(path, bin, backup)
			return r.Changed, len(r.Events), err
		},
		uninstall: func(path, backup string) (bool, error) {
			r, err := codex.Uninstall(path, backup)
			return r.Changed, err
		},
		installed: codex.Installed,
		note:      codex.TrustNote,
	},
}

func isUnparseable(err error) bool { return errors.Is(err, errUnparseable) }
