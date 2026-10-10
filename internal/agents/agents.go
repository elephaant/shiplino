// Package agents lists the agents Shiplino connects to through hook
// config files, for setup, uninstall, doctor and the settings page.
package agents

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/elephaant/shiplino/pkg/adapters/claudecode"
	"github.com/elephaant/shiplino/pkg/adapters/cline"
	"github.com/elephaant/shiplino/pkg/adapters/codex"
	"github.com/elephaant/shiplino/pkg/adapters/copilotcli"
	"github.com/elephaant/shiplino/pkg/adapters/cursor"
	"github.com/elephaant/shiplino/pkg/adapters/geminicli"
	"github.com/elephaant/shiplino/pkg/adapters/opencode"
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
	{
		// Same agent and hooks, another config file: the JetBrains plugin.
		// Its backups get their own folder (both files are hooks.json).
		Name: "Windsurf (JetBrains)", ID: windsurf.Name,
		Detect: func(_ context.Context, home string) (bool, string, string) {
			d := windsurf.DetectJetBrains(home)
			return d.Installed, d.Version, d.HooksPath
		},
		Install: func(path, bin, _, backup string) (bool, int, error) {
			r, err := windsurf.Install(path, bin, filepath.Join(backup, "jetbrains"))
			return r.Changed, len(r.Events), err
		},
		Uninstall: func(path, backup string) (bool, error) {
			r, err := windsurf.Uninstall(path, filepath.Join(backup, "jetbrains"))
			return r.Changed, err
		},
		Installed: windsurf.Installed,
		Note:      windsurf.NoteJetBrains,
	},
	{
		Name: "Copilot CLI", ID: copilotcli.Name,
		Detect: func(ctx context.Context, home string) (bool, string, string) {
			d := copilotcli.Detect(ctx, home)
			return d.Installed, d.Version, d.HooksPath
		},
		Install: func(path, bin, _, backup string) (bool, int, error) {
			r, err := copilotcli.Install(path, bin, backup)
			return r.Changed, len(r.Events), err
		},
		Uninstall: func(path, backup string) (bool, error) {
			r, err := copilotcli.Uninstall(path, backup)
			return r.Changed, err
		},
		Installed: copilotcli.Installed,
		Note:      copilotcli.RestartNote,
	},
	{
		Name: "Gemini CLI", ID: geminicli.Name,
		Detect: func(ctx context.Context, home string) (bool, string, string) {
			d := geminicli.Detect(ctx, home)
			return d.Installed, d.Version, d.SettingsPath
		},
		Install: func(path, bin, _, backup string) (bool, int, error) {
			r, err := geminicli.Install(path, bin, backup)
			return r.Changed, len(r.Events), err
		},
		Uninstall: func(path, backup string) (bool, error) {
			r, err := geminicli.Uninstall(path, backup)
			return r.Changed, err
		},
		Installed: geminicli.Installed,
	},
	{
		// A plugin file rather than hook config: OpenCode has no command hooks.
		Name: "OpenCode", ID: opencode.Name,
		Detect: func(ctx context.Context, home string) (bool, string, string) {
			d := opencode.Detect(ctx, home)
			return d.Installed, d.Version, d.PluginPath
		},
		Install: func(path, bin, _, backup string) (bool, int, error) {
			r, err := opencode.Install(path, bin, backup)
			return r.Changed, len(r.Events), err
		},
		Uninstall: func(path, backup string) (bool, error) {
			r, err := opencode.Uninstall(path, backup)
			return r.Changed, err
		},
		Installed: opencode.Installed,
		Note:      opencode.RestartNote,
	},
	{
		// Hook scripts in a directory, not a config file: path is the
		// directory. Events taken by the user's own scripts are an error,
		// so setup and doctor show them.
		Name: "Cline", ID: cline.Name,
		Detect: func(ctx context.Context, home string) (bool, string, string) {
			d := cline.Detect(ctx, home)
			return d.Installed, d.Version, d.HooksDir
		},
		Install:   clineInstall,
		Uninstall: clineUninstall,
		Installed: cline.Installed,
		Note:      cline.Note,
	},
	{
		// The extension's own Documents folder, when the OS moved it
		// (OneDrive, xdg-user-dirs); not found otherwise.
		Name: "Cline (VS Code)", ID: cline.Name,
		Detect: func(ctx context.Context, home string) (bool, string, string) {
			d := cline.DetectExtension(ctx, home)
			return d.Installed, d.Version, d.HooksDir
		},
		Install:   clineInstall,
		Uninstall: clineUninstall,
		Installed: cline.Installed,
		Note:      cline.NoteExtension,
	},
}

func clineInstall(dir, bin, _, _ string) (bool, int, error) {
	r, err := cline.Install(dir, bin)
	if err == nil {
		err = r.Err()
	}
	return r.Changed, len(r.Events), err
}

func clineUninstall(dir, _ string) (bool, error) {
	r, err := cline.Uninstall(dir)
	return r.Changed, err
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
