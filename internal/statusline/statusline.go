// Package statusline is `shiplino statusline`, the opt-in wrapper around
// Claude Code's status line command (`shiplino setup --statusline`).
//
// Claude Code passes plan usage (rate_limits) only to status line
// commands. The wrapper copies those numbers to the spool, the way the
// hook does, then runs the user's own command with the same input and
// lets its output and exit code through untouched: what the user sees
// stays byte-for-byte the same. Like the hook it never uses the network
// or the database, and nothing it fails at stops the user's command.
package statusline

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/elephaant/shiplino/internal/spool"
	"github.com/elephaant/shiplino/pkg/model"
)

// MaxInput caps how much of stdin is read for recording. Larger input is
// still passed to the user's command in full, just not recorded.
const MaxInput = 1 << 20

// agent and event name the spool lines (see the Claude Code adapter).
const (
	agent = "claude-code"
	event = "StatusLine"
)

// Run is the whole command: record, then run the wrapped command (if
// any) and return its exit code.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	wrapped, minimal, err := parseArgs(args)
	in, _ := io.ReadAll(io.LimitReader(stdin, MaxInput+1))
	full := len(in) <= MaxInput
	if full {
		record(in, time.Now())
	}
	if err != nil {
		// The command can't be read back: run nothing, say why where
		// `claude --debug` shows it, and let doctor explain.
		fmt.Fprintln(stderr, "shiplino statusline:", err)
		return 1
	}
	if wrapped == "" {
		if minimal && full {
			_, _ = io.WriteString(stdout, Minimal(in, time.Now()))
		}
		return 0
	}
	rest := io.MultiReader(bytes.NewReader(in), stdin)
	if full {
		rest = bytes.NewReader(in)
	}
	return runShell(wrapped, rest, stdout, stderr)
}

// parseArgs reads `--wrap <base64url>` and `--minimal`.
func parseArgs(args []string) (wrapped string, minimal bool, err error) {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--minimal":
			minimal = true
		case a == "--wrap" && i+1 < len(args):
			i++
			b, derr := base64.RawURLEncoding.DecodeString(args[i])
			if derr != nil {
				return "", false, fmt.Errorf("can't decode the wrapped status line command (run `shiplino doctor`): %w", derr)
			}
			wrapped = string(b)
		case strings.HasPrefix(a, "--wrap="):
			return parseArgs(append([]string{"--wrap", strings.TrimPrefix(a, "--wrap=")}, args[i+1:]...))
		}
	}
	return wrapped, minimal, nil
}

// input is the part of Claude Code's status line JSON Shiplino keeps:
// ids, the version and the plan windows. Nothing the user or the model
// wrote.
type input struct {
	SessionID  string `json:"session_id"`
	Version    string `json:"version"`
	Model      any    `json:"model"`
	RateLimits map[string]struct {
		UsedPercentage *float64 `json:"used_percentage"`
		ResetsAt       *float64 `json:"resets_at"`
	} `json:"rate_limits"`
}

type window struct {
	UsedPercentage float64  `json:"used_percentage"`
	ResetsAt       *float64 `json:"resets_at,omitempty"`
}

// line is what goes to the spool, shaped like a hook payload.
type line struct {
	HookEventName string            `json:"hook_event_name"`
	SessionID     string            `json:"session_id"`
	Version       string            `json:"version,omitempty"`
	Model         string            `json:"model,omitempty"`
	RateLimits    map[string]window `json:"rate_limits"`
}

// record appends the plan windows to the session's spool file with one
// O_APPEND write. Input without windows (API users, before the first
// response) writes nothing. Every failure is silent.
func record(in []byte, now time.Time) {
	defer func() { _ = recover() }()
	home := spool.Home()
	if home == "" || spool.Paused(home, now) {
		return
	}
	payload, session, ok := extract(in)
	if !ok {
		return
	}
	env := spool.Envelope{ID: model.NewULID(now), Agent: agent, Event: event, TS: now.UnixNano(), PID: os.Getpid()}
	_ = spool.Write(spool.Dir(home), session, env, payload)
}

// extract cuts the input down to the spool line.
func extract(in []byte) (payload []byte, session string, ok bool) {
	var p input
	if json.Unmarshal(in, &p) != nil || p.SessionID == "" {
		return nil, "", false
	}
	out := line{HookEventName: event, SessionID: p.SessionID, Version: p.Version, RateLimits: map[string]window{}}
	switch m := p.Model.(type) {
	case string:
		out.Model = m
	case map[string]any:
		out.Model, _ = m["id"].(string)
	}
	for name, w := range p.RateLimits {
		if (name == "five_hour" || strings.HasPrefix(name, "seven_day")) && w.UsedPercentage != nil {
			out.RateLimits[name] = window{UsedPercentage: *w.UsedPercentage, ResetsAt: w.ResetsAt}
		}
	}
	if len(out.RateLimits) == 0 {
		return nil, "", false
	}
	b, err := json.Marshal(out)
	return b, p.SessionID, err == nil
}

// Minimal is Shiplino's own status line, for users without one who asked
// for it: "5h 23% · 7d 41%", or nothing without plan windows.
func Minimal(in []byte, now time.Time) string {
	var p input
	if json.Unmarshal(in, &p) != nil {
		return ""
	}
	labels := map[string]string{"five_hour": "5h", "seven_day": "7d"}
	var names []string
	for name, w := range p.RateLimits {
		if labels[name] != "" && w.UsedPercentage != nil {
			names = append(names, name)
		}
	}
	sort.Strings(names) // five_hour before seven_day
	var parts []string
	for _, name := range names {
		w := p.RateLimits[name]
		s := fmt.Sprintf("%s %.0f%%", labels[name], *w.UsedPercentage)
		if w.ResetsAt != nil && name == "five_hour" {
			if t := time.Unix(int64(*w.ResetsAt), 0); t.After(now) {
				s += " · resets " + t.Local().Format("15:04")
			}
		}
		parts = append(parts, s)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " · ") + "\n"
}

// runShell runs cmd the way Claude Code runs a status line command: sh on
// macOS and Linux; Git Bash on Windows, or PowerShell without it. Output
// goes straight to ours (an *os.File is handed to the child as is).
func runShell(cmd string, stdin io.Reader, stdout, stderr io.Writer) int {
	name, args := shellFor(cmd)
	c := exec.Command(name, args...)
	c.Stdin, c.Stdout, c.Stderr = stdin, stdout, stderr
	if err := c.Start(); err != nil {
		fmt.Fprintln(stderr, "shiplino statusline: can't run the status line command:", err)
		return 127
	}
	stop := relaySignals(c.Process)
	err := c.Wait()
	stop()
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		if code := ee.ExitCode(); code >= 0 {
			return code
		}
		return 1 // killed by a signal
	}
	return 1
}

// shellFor is shell; tests replace it to run a helper without a shell.
var shellFor = shell

// shell returns the program and arguments that run cmd.
func shell(cmd string) (string, []string) {
	if runtime.GOOS != "windows" {
		return "/bin/sh", []string{"-c", cmd}
	}
	if bash := gitBash(); bash != "" {
		return bash, []string{"-c", cmd}
	}
	return "powershell.exe", []string{"-NoProfile", "-Command", cmd}
}
