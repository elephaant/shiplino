// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

package copilotcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"

	"github.com/elephaant/shiplino/pkg/adapters/internal/configfile"
	"github.com/elephaant/shiplino/pkg/adapters/internal/hookfile"
)

// Events we register: observe-only hooks (see the package doc).
var Events = []string{
	"sessionStart", "sessionEnd", "userPromptSubmitted", "agentStop",
	"postToolUse", "postToolUseFailure", "subagentStop", "preCompact",
	"errorOccurred", "notification",
}

// HooksFile is the file Shiplino owns in Copilot's user hooks directory.
const HooksFile = "shiplino.json"

// RestartNote is shown after installing.
const RestartNote = "Copilot CLI reads hooks when a session starts: restart running copilot sessions"

// Detection describes what was found on this machine.
type Detection struct {
	Installed bool
	Version   string
	HooksPath string
}

// Dir is Copilot CLI's config and state directory: $COPILOT_HOME, or
// ~/.copilot.
func Dir(home string) string {
	if d := os.Getenv("COPILOT_HOME"); d != "" {
		return d
	}
	return filepath.Join(home, ".copilot")
}

var awsCopilot = regexp.MustCompile(`(?i)copilot version: v`)

// Detect looks for Copilot CLI. ~/.copilot alone isn't enough: IDE
// integrations create it too. A copilot binary on PATH counts unless it's
// the unrelated AWS Copilot CLI, and so does Copilot's session history or
// hooks directory.
func Detect(ctx context.Context, home string) Detection {
	dir := Dir(home)
	d := Detection{HooksPath: filepath.Join(dir, "hooks", HooksFile)}
	for _, sub := range []string{"session-state", "hooks"} {
		if fi, err := os.Stat(filepath.Join(dir, sub)); err == nil && fi.IsDir() {
			d.Installed = true
		}
	}
	if bin, err := exec.LookPath("copilot"); err == nil {
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, bin, "--version").Output()
		if err == nil && !awsCopilot.Match(out) {
			d.Installed = true
			d.Version = regexp.MustCompile(`\d+\.\d+\.\d+`).FindString(string(out))
		}
	}
	return d
}

// Install writes our hooks to hooksPath (normally
// ~/.copilot/hooks/shiplino.json): command hooks only, a 5 s timeout, no
// matcher. bash and powershell both run `<bin> hook --agent copilot-cli
// --event <name>`.
func Install(hooksPath, binPath, backupDir string) (hookfile.Result, error) {
	if !filepath.IsAbs(binPath) {
		return hookfile.Result{}, fmt.Errorf("binary path must be absolute: %s", binPath)
	}
	return hookfile.InstallFlat(hooksPath, backupDir, Name, Events, func(event string) *configfile.Object {
		args := fmt.Sprintf(" hook --agent %s --event %s", Name, event)
		return &configfile.Object{Members: []configfile.Member{
			{Key: "type", Value: "command"},
			{Key: "bash", Value: hookfile.ShellQuote(binPath) + args},
			{Key: "powershell", Value: "& " + hookfile.PowerShellQuote(binPath) + args},
			{Key: "timeoutSec", Value: json.Number("5")},
		}}
	}, func(root *configfile.Object) {
		if _, ok := root.Get("version"); !ok {
			root.Members = append([]configfile.Member{{Key: "version", Value: json.Number("1")}}, root.Members...)
		}
	})
}

// Uninstall removes Shiplino's hooks, and the file itself when nothing
// else is left in it.
func Uninstall(hooksPath, backupDir string) (hookfile.Result, error) {
	res, err := hookfile.Uninstall(hooksPath, backupDir, Name)
	if err != nil {
		return res, err
	}
	b, err := os.ReadFile(hooksPath)
	if errors.Is(err, os.ErrNotExist) {
		return res, nil
	}
	if err != nil {
		return res, err
	}
	if hookfile.SameJSON(b, []byte(`{"version": 1}`)) || len(bytes.TrimSpace(b)) == 0 || hookfile.SameJSON(b, []byte(`{}`)) {
		if res.Backup == "" {
			if res.Backup, err = configfile.Backup(hooksPath, backupDir); err != nil {
				return res, fmt.Errorf("backup: %w", err)
			}
		}
		if err := os.Remove(hooksPath); err != nil {
			return res, err
		}
		res.Changed = true
	}
	return res, nil
}

// Installed reports whether hooksPath has our hook, and its command.
func Installed(hooksPath string) (bool, string, error) {
	return hookfile.Installed(hooksPath, Name)
}
