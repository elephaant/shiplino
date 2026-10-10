// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

package opencode

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
	opencodeplugin "github.com/elephaant/shiplino/plugins/opencode"
)

// Events are the OpenCode bus events the plugin records.
var Events = []string{
	"session.created", "session.updated", "session.deleted", "session.idle", "session.compacted",
	"session.error", "message.updated", "message.part.updated", "permission.asked",
	"permission.updated", "permission.replied", "question.asked", "question.replied", "question.rejected",
}

// PluginFile is the file Shiplino owns in OpenCode's global plugin folder.
const PluginFile = "shiplino.js"

// RestartNote is shown after installing.
const RestartNote = "OpenCode loads plugins when it starts: restart running opencode sessions"

// ErrNotOurs means a plugin file with our name exists but Shiplino didn't
// write it, so it was left untouched.
var ErrNotOurs = errors.New("plugin file exists and wasn't written by Shiplino; left untouched")

// Detection describes what was found on this machine.
type Detection struct {
	Installed  bool
	Version    string
	PluginPath string
}

// ConfigDir is OpenCode's global config directory: $XDG_CONFIG_HOME/opencode,
// or ~/.config/opencode (on Windows too).
func ConfigDir(home string) string { return xdg("XDG_CONFIG_HOME", home, ".config") }

// DataDir is OpenCode's data directory (sessions, logs).
func DataDir(home string) string { return xdg("XDG_DATA_HOME", home, ".local", "share") }

func xdg(env, home string, fallback ...string) string {
	if d := os.Getenv(env); d != "" {
		return filepath.Join(d, "opencode")
	}
	return filepath.Join(append(append([]string{home}, fallback...), "opencode")...)
}

var versionRe = regexp.MustCompile(`\d+\.\d+\.\d+`)

// Detect looks for an opencode binary on PATH, or OpenCode's config or
// data directory.
func Detect(ctx context.Context, home string) Detection {
	d := Detection{PluginPath: filepath.Join(ConfigDir(home), "plugins", PluginFile)}
	for _, dir := range []string{ConfigDir(home), DataDir(home)} {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			d.Installed = true
		}
	}
	if bin, err := exec.LookPath("opencode"); err == nil {
		d.Installed = true
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, bin, "--version").Output(); err == nil {
			d.Version = versionRe.FindString(string(out))
		}
	}
	return d
}

// Render is the plugin source with binPath filled in.
func Render(binPath string) []byte {
	q, _ := json.Marshal(binPath) // a valid JS string literal (U+2028/9 escaped)
	return bytes.Replace(opencodeplugin.Source, []byte(`"`+opencodeplugin.BinPlaceholder+`"`), q, 1)
}

// Install writes the plugin to pluginPath (normally
// ~/.config/opencode/plugins/shiplino.js), running binPath. An earlier
// copy of ours is replaced; a file of the same name that isn't ours is
// left alone (ErrNotOurs). Other plugins are never touched.
func Install(pluginPath, binPath, backupDir string) (hookfile.Result, error) {
	res := hookfile.Result{Path: pluginPath, Events: Events}
	if !filepath.IsAbs(binPath) {
		return res, fmt.Errorf("binary path must be absolute: %s", binPath)
	}
	want := Render(binPath)
	old, err := os.ReadFile(pluginPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return res, err
	case !bytes.Contains(old, []byte(opencodeplugin.Marker)):
		return res, fmt.Errorf("%s: %w", pluginPath, ErrNotOurs)
	case bytes.Equal(old, want):
		return res, nil
	default:
		if res.Backup, err = configfile.Backup(pluginPath, backupDir); err != nil {
			return res, fmt.Errorf("backup: %w", err)
		}
	}
	if err := configfile.WriteAtomic(pluginPath, want); err != nil {
		return res, err
	}
	res.Changed = true
	return res, nil
}

// Uninstall removes Shiplino's plugin file, after a backup. Anything else
// in the folder, and a file of the same name that isn't ours, stays.
func Uninstall(pluginPath, backupDir string) (hookfile.Result, error) {
	res := hookfile.Result{Path: pluginPath}
	old, err := os.ReadFile(pluginPath)
	if errors.Is(err, os.ErrNotExist) {
		return res, nil
	}
	if err != nil {
		return res, err
	}
	if !bytes.Contains(old, []byte(opencodeplugin.Marker)) {
		return res, nil
	}
	if res.Backup, err = configfile.Backup(pluginPath, backupDir); err != nil {
		return res, fmt.Errorf("backup: %w", err)
	}
	if err := os.Remove(pluginPath); err != nil {
		return res, err
	}
	res.Changed = true
	return res, nil
}

var binRe = regexp.MustCompile(`(?m)^const BIN = ("(?:[^"\\]|\\.)*")`)

// Installed reports whether pluginPath is our plugin, and the command it
// runs. A foreign file with our name is reported as ErrNotOurs.
func Installed(pluginPath string) (bool, string, error) {
	b, err := os.ReadFile(pluginPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	if !bytes.Contains(b, []byte(opencodeplugin.Marker)) {
		return false, "", fmt.Errorf("%s: %w", pluginPath, ErrNotOurs)
	}
	bin := "shiplino"
	if m := binRe.FindSubmatch(b); m != nil {
		var s string
		if json.Unmarshal(m[1], &s) == nil && s != opencodeplugin.BinPlaceholder {
			bin = s
		}
	}
	return true, bin + " hook --agent " + Name, nil
}
