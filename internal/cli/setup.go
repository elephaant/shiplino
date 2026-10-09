// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/elephaant/shiplino/internal/api"
	"github.com/elephaant/shiplino/internal/spool"
	"github.com/elephaant/shiplino/pkg/adapters/claudecode"
)

// env is what commands need from the outside world; tests replace it.
type env struct {
	out, errOut io.Writer
	home        string // Shiplino home (~/.shiplino)
	userHome    string // the user's home directory
	self        string // path of the running binary
	version     string
}

func (e *env) binPath() string {
	name := "shiplino"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(e.home, "bin", name)
}

func (e *env) backupDir(agent string) string { return filepath.Join(e.home, "backups", agent) }

// setup installs the binary, connects detected agents and checks the hook.
func setup(ctx context.Context, e *env, args []string) int {
	fmt.Fprintf(e.out, "Shiplino %s setup\n\n", e.version)

	for _, d := range []string{"bin", "spool", "data", "backups", "logs"} {
		if err := os.MkdirAll(filepath.Join(e.home, d), 0o700); err != nil {
			fmt.Fprintf(e.errOut, "cannot create %s: %v\n", filepath.Join(e.home, d), err)
			return 1
		}
	}
	bin := e.binPath()
	if err := installBinary(e.self, bin); err != nil {
		fmt.Fprintf(e.errOut, "cannot install the binary to %s: %v\n", bin, err)
		return 1
	}
	if _, err := api.LoadToken(e.home); err != nil {
		fmt.Fprintf(e.errOut, "cannot create the API token: %v\n", err)
		return 1
	}

	ok := true
	connected := 0
	det := claudecode.Detect(ctx, e.userHome)
	switch {
	case !det.Installed:
		fmt.Fprintf(e.out, "  ➖ %-20s not found\n", "Claude Code")
	default:
		res, err := claudecode.Install(det.SettingsPath, bin, det.Version, e.backupDir(claudecode.Name))
		switch {
		case errors.Is(err, claudecode.ErrUnparseable):
			ok = false
			fmt.Fprintf(e.out, "  ⚠️  %-20s %s isn't plain JSON (comments?), left untouched. Add the hooks by hand: see README.\n", "Claude Code", tilde(det.SettingsPath, e.userHome))
		case err != nil:
			ok = false
			fmt.Fprintf(e.out, "  ❌ %-20s %v\n", "Claude Code", err)
		default:
			connected++
			what := "hooks already up to date"
			if res.Changed {
				what = fmt.Sprintf("hooks added for %d events", len(res.Events))
			}
			name := "Claude Code"
			if det.Version != "" {
				name += " " + det.Version
			}
			fmt.Fprintf(e.out, "  ✅ %-20s %s (%s)\n", name, what, tilde(det.SettingsPath, e.userHome))
		}
	}

	if err := selfTest(ctx, e, bin); err != nil {
		ok = false
		fmt.Fprintf(e.out, "  ❌ %-20s %v\n", "Hook test", err)
	} else {
		fmt.Fprintf(e.out, "  ✅ %-20s prints nothing, exits 0\n", "Hook test")
	}

	fmt.Fprintln(e.out)
	switch {
	case connected == 0:
		fmt.Fprintln(e.out, "No supported agents were connected. Install one and run `shiplino setup` again.")
	default:
		fmt.Fprintln(e.out, "Start the daemon with `shiplino daemon`, then open http://localhost:4777.")
		fmt.Fprintln(e.out, "New agent sessions are recorded from now on. Restart running sessions to pick up the hooks.")
	}
	if !ok {
		return 1
	}
	return 0
}

// uninstall removes our hooks; with --purge it also deletes all data.
func uninstall(ctx context.Context, e *env, args []string) int {
	purge := hasFlag(args, "--purge")
	ok := true
	settings := filepath.Join(e.userHome, ".claude", "settings.json")
	if res, err := claudecode.Uninstall(settings, e.backupDir(claudecode.Name)); err != nil {
		ok = false
		fmt.Fprintf(e.out, "  ❌ Claude Code  %v\n", err)
	} else if res.Changed {
		fmt.Fprintf(e.out, "  ✅ Claude Code  hooks removed (%s)\n", tilde(settings, e.userHome))
	} else {
		fmt.Fprintln(e.out, "  ➖ Claude Code  no Shiplino hooks found")
	}
	if purge {
		if err := os.RemoveAll(e.home); err != nil {
			ok = false
			fmt.Fprintf(e.out, "  ❌ could not delete %s: %v\n", e.home, err)
		} else {
			fmt.Fprintf(e.out, "  ✅ deleted %s\n", e.home)
		}
	} else {
		fmt.Fprintf(e.out, "\nYour data is kept in %s (use --purge to delete it).\n", e.home)
	}
	if !ok {
		return 1
	}
	return 0
}

// installBinary copies the running binary to dst (atomically), unless it
// already runs from there. Hooks always point at dst, so updates replace
// it in place and agent configs never need editing again.
func installBinary(self, dst string) error {
	if s, err := filepath.EvalSymlinks(self); err == nil {
		self = s
	}
	if d, err := filepath.EvalSymlinks(dst); err == nil && d == self {
		return nil
	}
	src, err := os.ReadFile(self)
	if err != nil {
		return err
	}
	if cur, err := os.ReadFile(dst); err == nil && bytes.Equal(cur, src) {
		return nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".shiplino-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(src); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	tmp.Close()
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

// selfTest runs the installed hook exactly as an agent would and checks
// the zero-token contract: no output, exit 0, one spool line written.
func selfTest(ctx context.Context, e *env, bin string) error {
	const agent = "shiplino-selftest"
	cmd := exec.CommandContext(ctx, bin, "hook", "--agent", agent)
	cmd.Env = append(os.Environ(), "SHIPLINO_HOME="+e.home)
	cmd.Stdin = strings.NewReader(`{"session_id":"selftest","hook_event_name":"SessionStart"}`)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	dir := filepath.Join(spool.Dir(e.home), agent)
	defer os.RemoveAll(dir)
	if err != nil {
		return fmt.Errorf("hook failed: %v", err)
	}
	if out.Len() > 0 {
		return fmt.Errorf("hook printed output (would cost tokens): %q", out.String())
	}
	if _, err := os.Stat(spool.SessionFile(spool.Dir(e.home), agent, "selftest")); err != nil {
		return errors.New("hook didn't write to the spool")
	}
	return nil
}

func tilde(path, home string) string {
	if home != "" && strings.HasPrefix(path, home+string(filepath.Separator)) {
		return "~" + path[len(home):]
	}
	return path
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}
