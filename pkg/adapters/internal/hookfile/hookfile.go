// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

// Package hookfile edits agent hook configs that share the common shape
// {"hooks": {Event: [{matcher?, hooks: [handler…]}]}} (Claude Code
// settings.json, Codex hooks.json): add, update and remove Shiplino's
// handlers without touching anything else.
package hookfile

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/elephaant/shiplino/pkg/adapters/internal/configfile"
)

// ErrUnparseable means the file isn't plain JSON we can rewrite exactly
// (e.g. it has comments). It is left untouched.
var ErrUnparseable = errors.New("config file is not plain JSON; left untouched")

// Result reports what Install or Uninstall did.
type Result struct {
	Path    string
	Changed bool
	Backup  string   // backup of the previous file, if it was modified
	Events  []string // events our hook is registered for (Install)
}

// Install replaces any earlier Shiplino handlers for agent with one group
// per event built by handler, leaving everything else as it was.
func Install(path, backupDir, agent string, events []string, handler func(event string) *configfile.Object) (Result, error) {
	return edit(path, backupDir, func(hooks *configfile.Object) {
		removeOurs(hooks, agent)
		for _, ev := range events {
			groups, _ := hooks.Get(ev)
			list, _ := groups.([]any)
			list = append(list, &configfile.Object{Members: []configfile.Member{{Key: "hooks", Value: []any{handler(ev)}}}})
			hooks.Set(ev, list)
		}
	}, events)
}

// Uninstall removes only Shiplino's handlers for agent.
func Uninstall(path, backupDir, agent string) (Result, error) {
	return edit(path, backupDir, func(h *configfile.Object) { removeOurs(h, agent) }, nil)
}

// Installed reports whether the file has a Shiplino handler for agent,
// and its command.
func Installed(path, agent string) (bool, string, error) {
	b, err := os.ReadFile(path)
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
		for _, g := range AsList(m.Value) {
			for _, hk := range AsList(Field(g, "hooks")) {
				if IsOurs(hk, agent) {
					cmd, _ := Field(hk, "command").(string)
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
	if orig != nil && SameJSON(orig, out) {
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

// SameJSON compares documents ignoring whitespace.
func SameJSON(a, b []byte) bool {
	oa, err1 := configfile.ParseObject(a)
	ob, err2 := configfile.ParseObject(b)
	if err1 != nil || err2 != nil {
		return false
	}
	fa, _ := configfile.Format(oa)
	fb, _ := configfile.Format(ob)
	return bytes.Equal(fa, fb)
}

// removeOurs deletes Shiplino handlers, then groups and events left empty.
func removeOurs(hooks *configfile.Object, agent string) {
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
			handlers, ok := Field(gobj, "hooks").([]any)
			if !ok {
				outGroups = append(outGroups, g)
				continue
			}
			var keep []any
			for _, h := range handlers {
				if !IsOurs(h, agent) {
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

// IsOurs recognizes our handlers by command, in exec form
// (command=…/shiplino, args=[hook, --agent, <agent>]) or shell form.
func IsOurs(h any, agent string) bool {
	cmd, _ := Field(h, "command").(string)
	if args := AsList(Field(h, "args")); len(args) > 0 {
		var parts []string
		for _, a := range args {
			s, _ := a.(string)
			parts = append(parts, s)
		}
		cmd += " " + strings.Join(parts, " ")
	}
	return ourCommand(agent).MatchString(cmd)
}

// ourCommand matches `…shiplino[.exe] hook … --agent <agent>` in exec or
// shell form, with the binary path optionally quoted.
func ourCommand(agent string) *regexp.Regexp {
	return regexp.MustCompile(`(?:^|[\\/"' ])shiplino(?:\.exe)?["']? +hook +(?:.* )?--agent[ =]` + regexp.QuoteMeta(agent) + `(?:\s|$)`)
}

// Field reads key from a JSON object value.
func Field(v any, key string) any {
	o, ok := v.(*configfile.Object)
	if !ok {
		return nil
	}
	x, _ := o.Get(key)
	return x
}

// AsList reads a JSON array value.
func AsList(v any) []any {
	l, _ := v.([]any)
	return l
}
