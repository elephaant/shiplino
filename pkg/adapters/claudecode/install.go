// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package claudecode

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
	"strconv"
	"strings"
	"time"

	"github.com/elephaant/shiplino/pkg/adapters/internal/configfile"
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
type Result struct {
	Path    string
	Changed bool
	Backup  string   // backup of the previous file, if it was modified
	Events  []string // events our hook is registered for (Install)
}

// ErrUnparseable means the settings file isn't plain JSON we can rewrite
// exactly (e.g. it has comments). It is left untouched.
var ErrUnparseable = errors.New("settings file is not plain JSON; left untouched")

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
	return edit(settingsPath, backupDir, func(hooks *configfile.Object) {
		removeOurs(hooks)
		for _, ev := range events {
			groups, _ := hooks.Get(ev)
			list, _ := groups.([]any)
			list = append(list, ourGroup(binPath))
			hooks.Set(ev, list)
		}
	}, events)
}

// Uninstall removes only Shiplino's hook entries.
func Uninstall(settingsPath, backupDir string) (Result, error) {
	return edit(settingsPath, backupDir, removeOurs, nil)
}

// Installed reports whether the settings contain a Shiplino hook, and
// which binary it points to.
func Installed(settingsPath string) (bool, string, error) {
	b, err := os.ReadFile(settingsPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	root, err := configfile.ParseObject(b)
	if err != nil {
		return false, "", ErrUnparseable
	}
	hooks, _ := root.Get("hooks")
	h, ok := hooks.(*configfile.Object)
	if !ok {
		return false, "", nil
	}
	for _, m := range h.Members {
		for _, g := range asList(m.Value) {
			for _, hk := range asList(field(g, "hooks")) {
				if isOurs(hk) {
					cmd, _ := field(hk, "command").(string)
					return true, cmd, nil
				}
			}
		}
	}
	return false, "", nil
}

func edit(path, backupDir string, change func(*configfile.Object), events []string) (Result, error) {
	res := Result{Path: path, Events: events}
	orig, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return res, err
	}
	root := &configfile.Object{}
	if len(bytes.TrimSpace(orig)) > 0 {
		if root, err = configfile.ParseObject(orig); err != nil {
			return res, fmt.Errorf("%w: %v", ErrUnparseable, err)
		}
	}
	hooksVal, _ := root.Get("hooks")
	hooks, ok := hooksVal.(*configfile.Object)
	if hooksVal != nil && !ok {
		return res, fmt.Errorf("%w: \"hooks\" is not an object", ErrUnparseable)
	}
	if hooks == nil {
		hooks = &configfile.Object{}
	}
	change(hooks)
	if len(hooks.Members) == 0 {
		root.Delete("hooks")
	} else {
		root.Set("hooks", hooks)
	}

	out, err := configfile.Format(root)
	if err != nil {
		return res, err
	}
	if orig != nil && sameJSON(orig, out) {
		return res, nil // nothing to do: don't touch the file at all
	}
	if orig == nil && len(root.Members) == 0 {
		return res, nil
	}
	if res.Backup, err = configfile.Backup(path, backupDir); err != nil {
		return res, fmt.Errorf("backup: %w", err)
	}
	if err := configfile.WriteAtomic(path, out); err != nil {
		return res, err
	}
	res.Changed = true
	return res, nil
}

// sameJSON compares documents ignoring whitespace.
func sameJSON(a, b []byte) bool {
	oa, err1 := configfile.ParseObject(a)
	ob, err2 := configfile.ParseObject(b)
	if err1 != nil || err2 != nil {
		return false
	}
	fa, _ := configfile.Format(oa)
	fb, _ := configfile.Format(ob)
	return bytes.Equal(fa, fb)
}

func ourGroup(bin string) *configfile.Object {
	handler := &configfile.Object{Members: []configfile.Member{
		{Key: "type", Value: "command"},
		{Key: "command", Value: bin},
		{Key: "args", Value: []any{"hook", "--agent", Name}},
		{Key: "async", Value: true},
		{Key: "timeout", Value: jsonNumber(5)},
	}}
	return &configfile.Object{Members: []configfile.Member{{Key: "hooks", Value: []any{handler}}}}
}

// removeOurs deletes Shiplino handlers, then groups and events left empty.
func removeOurs(hooks *configfile.Object) {
	kept := hooks.Members[:0]
	for _, m := range hooks.Members {
		groups, isList := m.Value.([]any)
		if !isList {
			kept = append(kept, m)
			continue
		}
		var outGroups []any
		for _, g := range groups {
			gobj, ok := g.(*configfile.Object)
			if !ok {
				outGroups = append(outGroups, g)
				continue
			}
			handlers, ok := field(gobj, "hooks").([]any)
			if !ok {
				outGroups = append(outGroups, g)
				continue
			}
			var keep []any
			for _, h := range handlers {
				if !isOurs(h) {
					keep = append(keep, h)
				}
			}
			if len(keep) == len(handlers) {
				outGroups = append(outGroups, g)
			} else if len(keep) > 0 {
				gobj.Set("hooks", keep)
				outGroups = append(outGroups, gobj)
			}
		}
		if len(outGroups) > 0 {
			m.Value = outGroups
			kept = append(kept, m)
		}
	}
	hooks.Members = kept
}

// isOurs recognizes our handlers by command, in exec form
// (command=…/shiplino, args=[hook, --agent, claude-code]) or shell form.
func isOurs(h any) bool {
	cmd, _ := field(h, "command").(string)
	if args := asList(field(h, "args")); len(args) > 0 {
		var parts []string
		for _, a := range args {
			s, _ := a.(string)
			parts = append(parts, s)
		}
		cmd += " " + strings.Join(parts, " ")
	}
	return ourCommand.MatchString(cmd)
}

var ourCommand = regexp.MustCompile(`(?:^|[\\/"' ])shiplino(?:\.exe)?["']? +hook +(?:.* )?--agent[ =]` + regexp.QuoteMeta(Name) + `(?:\s|$)`)

func field(v any, key string) any {
	o, ok := v.(*configfile.Object)
	if !ok {
		return nil
	}
	x, _ := o.Get(key)
	return x
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
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
