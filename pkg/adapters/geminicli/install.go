package geminicli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/elephaant/shiplino/pkg/adapters/internal/configfile"
	"github.com/elephaant/shiplino/pkg/adapters/internal/hookfile"
)

// Hook config: the user settings file ~/.gemini/settings.json (the base
// directory moves with GEMINI_CLI_HOME), key "hooks", shaped like Claude
// Code's: event → [{matcher?, sequential?, hooks: [handler]}]. A handler
// is {type: "command", command, name?, timeout (milliseconds)}. Gemini CLI
// runs `command` through bash -c, or PowerShell on Windows. User-level
// hooks need no trust prompt (only project hooks do), and the hooks system
// is on unless hooksConfig.enabled is false. Settings may hold comments
// (JSONC); such a file is left untouched. Checked 2026-10-10.
//
// Hooks run synchronously and Gemini CLI reads their stdout (and, if
// stdout is empty, stderr) as JSON. With no output and exit code 0 there
// is nothing to apply: no decision, no rewritten input, no added context.
// The shim always exits 0 silently, so every event below only observes:
//
//	SessionStart, SessionEnd, PreCompress, Notification: advisory only;
//	  the CLI ignores flow control for them (SessionEnd isn't awaited).
//	BeforeAgent: could block the turn or add context, but only through
//	  output or exit code 2. Safe.
//	BeforeTool: could deny or rewrite the call, again only through output
//	  or exit code 2. Safe.
//	AfterTool, AfterAgent: could hide a result or force a retry, likewise
//	  only through output. Safe.
//
// Not registered: BeforeModel, AfterModel and BeforeToolSelection. Each
// carries the whole model request, AfterModel fires once per streamed
// chunk (a synchronous process per chunk would slow the agent down), and
// the transcript already records tokens and the model.

// Events we register.
var Events = []string{
	"SessionStart", "SessionEnd", "BeforeAgent", "AfterAgent",
	"BeforeTool", "AfterTool", "Notification", "PreCompress",
}

// hookName identifies our handler in /hooks and hooksConfig.disabled.
const hookName = "shiplino"

// Detection describes what was found on this machine.
type Detection struct {
	Installed    bool
	Version      string
	SettingsPath string
}

// configDir is Gemini CLI's global directory: GEMINI_CLI_HOME (if set)
// replaces the home directory.
func configDir(home string) string {
	if h := os.Getenv("GEMINI_CLI_HOME"); h != "" {
		home = h
	}
	return filepath.Join(home, ".gemini")
}

// Detect looks for Gemini CLI: the `gemini` binary or its settings file.
// ~/.gemini alone isn't enough, since other Google tools share it.
func Detect(ctx context.Context, home string) Detection {
	d := Detection{SettingsPath: filepath.Join(configDir(home), "settings.json")}
	if _, err := os.Stat(d.SettingsPath); err == nil {
		d.Installed = true
	}
	if bin, err := exec.LookPath("gemini"); err == nil {
		d.Installed = true
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, bin, "--version").Output(); err == nil {
			d.Version = regexp.MustCompile(`\d+\.\d+\.\d+`).FindString(string(out))
		}
	}
	return d
}

// Command is the hook command for binPath on goos: Gemini CLI runs hooks
// through bash, or PowerShell on Windows, where a quoted path is only a
// string until `&` calls it.
func Command(binPath, goos string) string {
	if goos == "windows" {
		return "& '" + strings.ReplaceAll(binPath, "'", "''") + "' hook --agent " + Name
	}
	return hookfile.ShellQuote(binPath) + " hook --agent " + Name
}

// Install registers our hook for every event in Events.
func Install(settingsPath, binPath, backupDir string) (hookfile.Result, error) {
	if !filepath.IsAbs(binPath) {
		return hookfile.Result{}, fmt.Errorf("binary path must be absolute: %s", binPath)
	}
	cmd := Command(binPath, runtime.GOOS)
	return hookfile.Install(settingsPath, backupDir, Name, Events, func(string) *configfile.Object {
		return &configfile.Object{Members: []configfile.Member{
			{Key: "name", Value: hookName},
			{Key: "type", Value: "command"},
			{Key: "command", Value: cmd},
			{Key: "timeout", Value: json.Number("5000")},
		}}
	})
}

// Uninstall removes only Shiplino's handlers.
func Uninstall(settingsPath, backupDir string) (hookfile.Result, error) {
	return hookfile.Uninstall(settingsPath, backupDir, Name)
}

// ErrHooksOff means our hook is installed but Gemini CLI won't run it.
var ErrHooksOff = errors.New("hooks are turned off in Gemini CLI")

// Installed reports whether settings.json has our handler, and its
// command. If Gemini CLI's own settings turn hooks (or ours) off, it
// returns ErrHooksOff so doctor can say so.
func Installed(settingsPath string) (bool, string, error) {
	ok, cmd, err := hookfile.Installed(settingsPath, Name)
	if !ok || err != nil {
		return ok, cmd, err
	}
	b, _ := os.ReadFile(settingsPath)
	root, err := configfile.ParseObject(b)
	if err != nil {
		return ok, cmd, nil
	}
	cfg, _ := root.Get("hooksConfig")
	if on, isBool := hookfile.Field(cfg, "enabled").(bool); isBool && !on {
		return ok, cmd, fmt.Errorf("%w: hooksConfig.enabled is false in %s", ErrHooksOff, settingsPath)
	}
	for _, d := range hookfile.AsList(hookfile.Field(cfg, "disabled")) {
		if d == hookName || d == cmd {
			return ok, cmd, fmt.Errorf("%w: hooksConfig.disabled in %s lists Shiplino's hook", ErrHooksOff, settingsPath)
		}
	}
	return ok, cmd, nil
}
