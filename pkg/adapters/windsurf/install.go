// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

package windsurf

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/elephaant/shiplino/pkg/adapters/internal/configfile"
	"github.com/elephaant/shiplino/pkg/adapters/internal/hookfile"
)

// Events we register: observe-only hooks (see the package doc).
var Events = []string{
	"pre_user_prompt", "post_cascade_response",
	"post_read_code", "post_write_code", "post_run_command", "post_mcp_tool_use",
}

// Note is shown after a fresh install.
const Note = "Windsurf: restart Windsurf so Cascade loads the new hooks. Hooks don't run in Restricted Mode workspaces."

// Detection describes what was found on this machine.
type Detection struct {
	Installed bool
	Version   string
	HooksPath string
}

// Detect looks for Windsurf under home or on PATH.
func Detect(ctx context.Context, home string) Detection {
	d := Detection{HooksPath: filepath.Join(home, ".codeium", "windsurf", "hooks.json")}
	if fi, err := os.Stat(filepath.Dir(d.HooksPath)); err == nil && fi.IsDir() {
		d.Installed = true
	}
	if bin, err := exec.LookPath("windsurf"); err == nil {
		d.Installed = true
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		out, err := exec.CommandContext(ctx, bin, "--version").Output()
		cancel()
		if m := regexp.MustCompile(`\d+\.\d+\.\d+`).FindString(string(out)); err == nil {
			d.Version = m
		}
	}
	return d
}

// NoteJetBrains is shown after a fresh install for the JetBrains plugin.
const NoteJetBrains = "Windsurf (JetBrains): restart the IDE so the Windsurf plugin loads the new hooks."

// DetectJetBrains looks for the Windsurf JetBrains plugin, whose hooks
// live in ~/.codeium/hooks.json. It counts as found when that file exists
// or a JetBrains IDE has a Windsurf/Codeium plugin folder. ~/.codeium on
// its own isn't enough: the Windsurf editor creates it too.
func DetectJetBrains(home string) Detection {
	d := Detection{HooksPath: filepath.Join(home, ".codeium", "hooks.json")}
	if _, err := os.Stat(d.HooksPath); err == nil {
		d.Installed = true
		return d
	}
	for _, root := range jetBrainsRoots(home) {
		// Linux keeps plugins directly in the product folder, macOS and
		// Windows in its plugins/ subfolder.
		for _, pat := range []string{filepath.Join(root, "*", "*"), filepath.Join(root, "*", "plugins", "*")} {
			dirs, _ := filepath.Glob(pat)
			for _, p := range dirs {
				name := strings.ToLower(filepath.Base(p))
				if strings.Contains(name, "codeium") || strings.Contains(name, "windsurf") {
					d.Installed = true
					return d
				}
			}
		}
	}
	return d
}

// jetBrainsRoots are where JetBrains IDEs keep per-product folders.
func jetBrainsRoots(home string) []string {
	return []string{
		filepath.Join(home, ".local", "share", "JetBrains"),                // Linux
		filepath.Join(home, "Library", "Application Support", "JetBrains"), // macOS
		filepath.Join(home, "AppData", "Roaming", "JetBrains"),             // Windows
	}
}

// Install registers `"<bin>" hook --agent windsurf` for Events in the
// user-level hooks.json, with show_output off. On Windows it also sets
// `powershell`, because PowerShell can't run a quoted path without `&`.
func Install(hooksPath, binPath, backupDir string) (hookfile.Result, error) {
	if !filepath.IsAbs(binPath) {
		return hookfile.Result{}, fmt.Errorf("binary path must be absolute: %s", binPath)
	}
	return hookfile.InstallFlat(hooksPath, backupDir, Name, Events, func(string) *configfile.Object {
		return handler(binPath, filepath.Separator == '\\')
	}, nil)
}

func handler(binPath string, windows bool) *configfile.Object {
	cmd := fmt.Sprintf("%s hook --agent %s", hookfile.ShellQuote(binPath), Name)
	h := &configfile.Object{Members: []configfile.Member{{Key: "command", Value: cmd}}}
	if windows {
		h.Members = append(h.Members, configfile.Member{Key: "powershell", Value: "& " + cmd})
	}
	h.Members = append(h.Members, configfile.Member{Key: "show_output", Value: false})
	return h
}

// Uninstall removes only Shiplino's handlers.
func Uninstall(hooksPath, backupDir string) (hookfile.Result, error) {
	return hookfile.Uninstall(hooksPath, backupDir, Name)
}

// Installed reports whether hooks.json has our handler, and its command.
func Installed(hooksPath string) (bool, string, error) {
	return hookfile.Installed(hooksPath, Name)
}
