// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"

	"github.com/elephaant/shiplino/pkg/adapters/internal/configfile"
	"github.com/elephaant/shiplino/pkg/adapters/internal/hookfile"
)

// Hook config: ~/.codex/hooks.json (also read alongside config.toml's
// [hooks]), same shape as Claude Code's: event → [{matcher?, hooks:
// [handler]}]. Codex runs `command` through a shell and has no args
// field, so the binary path is quoted. Checked 2026-10-10.

// Events we register: all of them.
var Events = []string{
	"SessionStart", "SessionEnd", "UserPromptSubmit", "Stop", "Interrupt",
	"PreToolUse", "PostToolUse", "PermissionRequest",
	"SubagentStart", "SubagentStop", "PreCompact", "PostCompact",
}

// syncEvents always run synchronously in Codex with a 1 s default
// (3 s max) timeout.
var syncEvents = map[string]bool{"SessionEnd": true, "Interrupt": true}

// Detection describes what was found on this machine.
type Detection struct {
	Installed bool
	Version   string
	HooksPath string
}

// Detect looks for Codex under home.
func Detect(ctx context.Context, home string) Detection {
	d := Detection{HooksPath: filepath.Join(home, ".codex", "hooks.json")}
	if fi, err := os.Stat(filepath.Join(home, ".codex")); err == nil && fi.IsDir() {
		d.Installed = true
	}
	if bin, err := exec.LookPath("codex"); err == nil {
		d.Installed = true
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, bin, "--version").Output(); err == nil {
			if m := regexp.MustCompile(`\d+\.\d+\.\d+`).FindString(string(out)); m != "" {
				d.Version = m
			}
		}
	}
	return d
}

// Install registers `"<bin>" hook --agent codex` for every event.
func Install(hooksPath, binPath, backupDir string) (hookfile.Result, error) {
	if !filepath.IsAbs(binPath) {
		return hookfile.Result{}, fmt.Errorf("binary path must be absolute: %s", binPath)
	}
	cmd := fmt.Sprintf("%s hook --agent %s", hookfile.ShellQuote(binPath), Name)
	return hookfile.Install(hooksPath, backupDir, Name, Events, func(event string) *configfile.Object {
		h := &configfile.Object{Members: []configfile.Member{
			{Key: "type", Value: "command"},
			{Key: "command", Value: cmd},
		}}
		if syncEvents[event] {
			h.Set("timeout", json.Number("1"))
		} else {
			h.Set("timeout", json.Number("5"))
			h.Set("async", true)
		}
		return h
	})
}

// Uninstall removes only Shiplino's handlers.
func Uninstall(hooksPath, backupDir string) (hookfile.Result, error) {
	return hookfile.Uninstall(hooksPath, backupDir, Name)
}

// Installed reports whether hooks.json has our handler, and its command.
func Installed(hooksPath string) (bool, string, error) {
	return hookfile.Installed(hooksPath, Name)
}

// TrustNote is shown after installing: Codex asks once before running
// new non-managed hooks.
const TrustNote = "Codex asks once before running new hooks: open Codex, run /hooks and trust the Shiplino hook"
