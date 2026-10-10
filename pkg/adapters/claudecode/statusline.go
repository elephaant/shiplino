package claudecode

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/elephaant/shiplino/pkg/adapters"
	"github.com/elephaant/shiplino/pkg/adapters/internal/configfile"
	"github.com/elephaant/shiplino/pkg/adapters/internal/hookfile"
	"github.com/elephaant/shiplino/pkg/model"
)

// The status line, checked against the official docs
// (https://code.claude.com/docs/en/statusline) on 2026-10-11:
//
//   - settings.json → "statusLine": {type: "command", command, padding?,
//     refreshInterval?, hideVimModeIndicator?}. The command runs in a shell:
//     sh on macOS and Linux; Git Bash on Windows, or PowerShell without it.
//   - It gets the session as JSON on stdin and shows what it prints. It runs
//     locally and adds no tokens: its output is shown, never sent to the
//     model.
//   - rate_limits.five_hour and .seven_day hold used_percentage (0-100) and
//     resets_at (Unix seconds). They're present only on Pro and Max plans,
//     after the session's first response; each window may be absent.
//
// It's the only place Claude Code reports how much of a plan window is
// used, so Shiplino can wrap the user's status line command (opt-in):
// `<shiplino> statusline --wrap <original, base64>` records the limits
// and runs the original with the same input, passing its output through
// unchanged. The original is kept in the command itself, so removing the
// wrapper restores it exactly even without Shiplino's own files.

// StatusLineArg is the subcommand the status line runs.
const StatusLineArg = "statusline"

// StatusLine is the state of the wrapper in the user's settings.
type StatusLine struct {
	Installed bool
	Bin       string // the Shiplino binary it runs (~ expanded)
	Original  string // the user's own command it wraps ("" if none)
	Minimal   bool   // no command of the user's: shows Shiplino's own line
	// Other is the user's own status line command when the wrapper isn't
	// installed ("" if none).
	Other string
}

// ErrStatusLineNotCommand means statusLine isn't a command Shiplino can
// wrap (an unknown type, or no command string). It is left untouched.
var ErrStatusLineNotCommand = errors.New(`"statusLine" isn't a {"type": "command"} entry Shiplino can wrap; left untouched`)

// ourStatusLine matches our command: the binary (bare, '…' or "…"), then
// `statusline`, then --wrap <base64url> or --minimal.
var ourStatusLine = regexp.MustCompile(`^('[^']*'|"[^"]*"|\S+) +` + StatusLineArg + `(?: +--wrap +([A-Za-z0-9_-]*)| +(--minimal))? *$`)

// StatusLineCommand is the command that runs bin as the wrapper of
// original ("" for none). minimal shows Shiplino's own short line when
// there is no original.
func StatusLineCommand(bin, home, original string, minimal bool) string {
	cmd := shellPath(bin, home) + " " + StatusLineArg
	switch {
	case original != "":
		cmd += " --wrap " + base64.RawURLEncoding.EncodeToString([]byte(original))
	case minimal:
		cmd += " --minimal"
	}
	return cmd
}

// ParseStatusLineCommand reads our wrapper command. ok is false when cmd
// isn't ours (or its --wrap value doesn't decode: then it isn't touched).
func ParseStatusLineCommand(cmd, home string) (st StatusLine, ok bool) {
	m := ourStatusLine.FindStringSubmatch(strings.TrimSpace(cmd))
	if m == nil {
		return st, false
	}
	bin := strings.Trim(m[1], `'"`)
	base := strings.TrimSuffix(strings.ToLower(filepath.Base(filepath.FromSlash(bin))), ".exe")
	if base != "shiplino" {
		return st, false
	}
	orig, err := base64.RawURLEncoding.DecodeString(m[2])
	if err != nil {
		return st, false
	}
	if strings.HasPrefix(bin, "~/") && home != "" {
		bin = filepath.Join(home, filepath.FromSlash(bin[2:]))
	}
	return StatusLine{Installed: true, Bin: filepath.FromSlash(bin), Original: string(orig), Minimal: m[3] != ""}, true
}

// shellPath writes bin for the shells Claude Code runs status lines in:
// forward slashes (Git Bash drops backslashes), and no quotes when it
// can do without, since PowerShell would read a quoted path as a string.
// A path under the home folder is written ~/… there (Claude Code expands
// it), which avoids quoting a user name with spaces on Windows.
func shellPath(bin, home string) string {
	p := filepath.ToSlash(bin)
	if bare(p) {
		return p
	}
	if home != "" {
		if rel, err := filepath.Rel(home, bin); err == nil && !strings.HasPrefix(rel, "..") {
			if r := "~/" + filepath.ToSlash(rel); bare(r[2:]) {
				return r
			}
		}
	}
	if runtime.GOOS == "windows" {
		return `"` + p + `"`
	}
	return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
}

var bareRE = regexp.MustCompile(`^[A-Za-z0-9_./:+-]+$`)

func bare(s string) bool { return bareRE.MatchString(s) }

// ReadStatusLine reports whether the wrapper is installed in the settings.
func ReadStatusLine(settingsPath, home string) (StatusLine, error) {
	b, err := os.ReadFile(settingsPath)
	if errors.Is(err, os.ErrNotExist) {
		return StatusLine{}, nil
	}
	if err != nil {
		return StatusLine{}, err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return StatusLine{}, nil
	}
	root, err := configfile.ParseObject(b)
	if err != nil {
		return StatusLine{}, ErrUnparseable
	}
	sl, cmd, err := statusLineOf(root)
	if err != nil || sl == nil {
		return StatusLine{}, err
	}
	if st, ok := ParseStatusLineCommand(cmd, home); ok {
		return st, nil
	}
	return StatusLine{Other: cmd}, nil
}

// statusLineOf returns the statusLine object and its command (nil when
// there is none).
func statusLineOf(root *configfile.Object) (*configfile.Object, string, error) {
	v, ok := root.Get("statusLine")
	if !ok || v == nil {
		return nil, "", nil
	}
	sl, ok := v.(*configfile.Object)
	if !ok {
		return nil, "", ErrStatusLineNotCommand
	}
	typ, _ := hookfile.Field(sl, "type").(string)
	cmd, isStr := hookfile.Field(sl, "command").(string)
	if typ != "command" || !isStr {
		return nil, "", ErrStatusLineNotCommand
	}
	return sl, cmd, nil
}

// InstallStatusLine wraps the user's status line command with
// `<bin> statusline`, or adds one that prints nothing (Shiplino's own
// short line with minimal) when there is none. Other statusLine keys
// (padding, refreshInterval, …) stay. Installing again only points it at
// bin. bin must be absolute; home is the user's home folder.
func InstallStatusLine(settingsPath, bin, home, backupDir string, minimal bool) (Result, error) {
	if !filepath.IsAbs(bin) {
		return Result{}, fmt.Errorf("binary path must be absolute: %s", bin)
	}
	return editStatusLine(settingsPath, backupDir, func(root, sl *configfile.Object, cmd string) error {
		if sl == nil {
			root.Set("statusLine", &configfile.Object{Members: []configfile.Member{
				{Key: "type", Value: "command"},
				{Key: "command", Value: StatusLineCommand(bin, home, "", minimal)},
			}})
			return nil
		}
		orig, keepMinimal := cmd, minimal
		if st, ok := ParseStatusLineCommand(cmd, home); ok {
			orig, keepMinimal = st.Original, st.Minimal || minimal
		}
		sl.Set("command", StatusLineCommand(bin, home, orig, keepMinimal))
		return nil
	})
}

// UninstallStatusLine puts the user's own command back exactly as it was,
// or removes the statusLine Shiplino added. Anything else is left alone.
func UninstallStatusLine(settingsPath, home, backupDir string) (Result, error) {
	return editStatusLine(settingsPath, backupDir, func(root, sl *configfile.Object, cmd string) error {
		if sl == nil {
			return nil
		}
		st, ok := ParseStatusLineCommand(cmd, home)
		switch {
		case !ok:
		case st.Original == "":
			root.Delete("statusLine")
		default:
			sl.Set("command", st.Original)
		}
		return nil
	})
}

// editStatusLine is hookfile's edit for the statusLine key: parse or don't
// touch, back up, then write atomically, and only when something changed.
func editStatusLine(path, backupDir string, change func(root, sl *configfile.Object, cmd string) error) (Result, error) {
	res := Result{Path: path}
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
	sl, cmd, err := statusLineOf(root)
	if err != nil {
		return res, err
	}
	if err := change(root, sl, cmd); err != nil {
		return res, err
	}
	out, err := configfile.Format(root)
	if err != nil {
		return res, err
	}
	if orig != nil && hookfile.SameJSON(orig, out) || orig == nil && len(root.Members) == 0 {
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

// StatusLineEvent is the event name of status line input in the spool.
const StatusLineEvent = "StatusLine"

// statusLineInput is the part of a StatusLine line read here. Windows are
// decoded one by one, so a malformed one doesn't hide the others.
type statusLineInput struct {
	Version    string                     `json:"version"`
	RateLimits map[string]json.RawMessage `json:"rate_limits"`
}

type statusLineLimit struct {
	UsedPercentage *float64 `json:"used_percentage"`
	ResetsAt       float64  `json:"resets_at"` // Unix seconds
}

// statusLineLimits turns the rate_limits the status line got into limit
// events: Claude Code's own percentages (limit_source: reported). The
// windows belong to the account, so the dedup key is global: the same
// numbers seen by several sessions, or on every refresh, are stored once.
func (b builder) statusLineLimits(raw []byte) []model.Event {
	var in statusLineInput
	_ = json.Unmarshal(raw, &in) // fields that don't fit stay empty
	names := make([]string, 0, len(in.RateLimits))
	for k := range in.RateLimits {
		names = append(names, k)
	}
	sort.Strings(names)
	var out []model.Event
	for _, name := range names {
		var minutes int64
		var id string
		switch {
		case name == "five_hour":
			minutes = 300
		case strings.HasPrefix(name, "seven_day"):
			minutes, id = 10080, strings.TrimPrefix(strings.TrimPrefix(name, "seven_day"), "_")
		default:
			continue // spend_limit and others aren't plan windows
		}
		var w statusLineLimit
		if json.Unmarshal(in.RateLimits[name], &w) != nil || w.UsedPercentage == nil || *w.UsedPercentage < 0 {
			continue
		}
		var resets time.Time
		if w.ResetsAt > 0 {
			resets = time.Unix(int64(w.ResetsAt), 0)
		}
		e := b.base(model.KindLimit, adapters.LimitData(minutes, id, *w.UsedPercentage, resets, ""))
		e.ActorID, e.ParentActor, e.ActorType, e.TurnID, e.Project = b.sid, "", "", "", nil
		e.Agent.Version = in.Version
		e.DedupKey = fmt.Sprintf("%s:limit:status:%s:%d:%g", Name, name, int64(w.ResetsAt), *w.UsedPercentage)
		out = append(out, e)
	}
	return out
}
