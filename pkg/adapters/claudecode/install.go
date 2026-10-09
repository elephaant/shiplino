// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package claudecode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/elephaant/shiplino/pkg/adapters/internal/configfile"
	"github.com/elephaant/shiplino/pkg/adapters/internal/hookfile"
)

// Hook configuration format checked against the official hooks reference
// (https://code.claude.com/docs/en/hooks) on 2026-10-10: settings.json →
// "hooks" → event → [{matcher?, hooks: [{type, command, args?, async?, timeout?}]}].
// We use exec form (command + args: no shell, safe with spaces and on
// Windows) and async, so the hook never blocks Claude Code.

// coreEvents exist in every Claude Code version we support.
var coreEvents = []string{
	"SessionStart", "SessionEnd", "UserPromptSubmit", "Stop",
	"PreToolUse", "PostToolUse", "Notification", "SubagentStop", "PreCompact",
}

// newerEvents are registered only on versions known to have them, so an
// older Claude Code never sees hook names it doesn't recognize.
var newerEvents = []string{
	"StopFailure", "PostToolUseFailure", "PermissionRequest", "PermissionDenied",
	"SubagentStart", "PostCompact",
}

// newerEventsSince is the first version assumed to support newerEvents.
var newerEventsSince = version{2, 1, 200}

// Detection describes what was found on this machine.
type Detection struct {
	Installed    bool   // binary on PATH or ~/.claude exists
	Version      string // "" if unknown
	SettingsPath string
}

// Detect looks for Claude Code under home.
func Detect(ctx context.Context, home string) Detection {
	d := Detection{SettingsPath: filepath.Join(home, ".claude", "settings.json")}
	if fi, err := os.Stat(filepath.Join(home, ".claude")); err == nil && fi.IsDir() {
		d.Installed = true
	}
	if bin, err := exec.LookPath("claude"); err == nil {
		d.Installed = true
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, bin, "--version").Output(); err == nil {
			d.Version = parseVersion(string(out)).String()
		}
	}
	return d
}

// Result reports what Install or Uninstall did.
type Result = hookfile.Result

// ErrUnparseable means the settings file isn't plain JSON we can rewrite
// exactly (e.g. it has comments). It is left untouched.
var ErrUnparseable = hookfile.ErrUnparseable

// Install registers `<bin> hook --agent claude-code` for every supported
// event in the user's settings, replacing any earlier Shiplino entries and
// leaving everything else as it was. binPath must be absolute.
func Install(settingsPath, binPath, claudeVersion, backupDir string) (Result, error) {
	if !filepath.IsAbs(binPath) {
		return Result{}, fmt.Errorf("binary path must be absolute: %s", binPath)
	}
	events := append([]string(nil), coreEvents...)
	if v := parseVersion(claudeVersion); !v.zero() && !v.less(newerEventsSince) {
		events = append(events, newerEvents...)
	}
	return hookfile.Install(settingsPath, backupDir, Name, events, func(string) *configfile.Object { return handler(binPath) })
}

// Uninstall removes only Shiplino's hook entries.
func Uninstall(settingsPath, backupDir string) (Result, error) {
	return hookfile.Uninstall(settingsPath, backupDir, Name)
}

// Installed reports whether the settings contain a Shiplino hook, and
// which binary it points to.
func Installed(settingsPath string) (bool, string, error) {
	return hookfile.Installed(settingsPath, Name)
}

func handler(bin string) *configfile.Object {
	return &configfile.Object{Members: []configfile.Member{
		{Key: "type", Value: "command"},
		{Key: "command", Value: bin},
		{Key: "args", Value: []any{"hook", "--agent", Name}},
		{Key: "async", Value: true},
		{Key: "timeout", Value: jsonNumber(5)},
	}}
}

func jsonNumber(n int) json.Number { return json.Number(strconv.Itoa(n)) }

type version [3]int

var versionRE = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

func parseVersion(s string) version {
	m := versionRE.FindStringSubmatch(s)
	if m == nil {
		return version{}
	}
	var v version
	for i := 0; i < 3; i++ {
		v[i], _ = strconv.Atoi(m[i+1])
	}
	return v
}

func (v version) zero() bool { return v == version{} }

func (v version) less(o version) bool {
	for i := 0; i < 3; i++ {
		if v[i] != o[i] {
			return v[i] < o[i]
		}
	}
	return false
}

func (v version) String() string {
	if v.zero() {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2])
}
