// Package config reads ~/.shiplino/config.toml.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/elephaant/shiplino/internal/notify"
	"github.com/elephaant/shiplino/pkg/redact"
)

// Config is the user's settings. Zero values mean defaults.
type Config struct {
	CaptureLevel string       `toml:"capture_level"`
	Redaction    Redaction    `toml:"redaction"`
	Notify       Notify       `toml:"notify"`
	Sync         Sync         `toml:"sync"`
	Budget       Budget       `toml:"budget"`
	Limits       Limits       `toml:"limits"`
	Integrations Integrations `toml:"integrations"`
	Service      Service      `toml:"service"`
	Update       Update       `toml:"update"`
	Git          Git          `toml:"git"`
}

// Git holds opt-in writes to the user's repositories. The daemon itself
// only ever reads them.
type Git struct {
	// Notes lets `shiplino git notes` write each recorded commit's line
	// authorship as a git note under refs/notes/shiplino. Notes are never
	// pushed.
	Notes bool `toml:"notes"`
}

// Service says how the daemon runs.
type Service struct {
	// Autostart: `shiplino setup` registered the daemon to start at login
	// (the default). false means the user runs `shiplino daemon` themselves
	// (`setup --no-service`), so a stopped daemon isn't an error.
	Autostart *bool `toml:"autostart"`
}

// Autostart reports whether the daemon is registered to start at login.
func (c Config) Autostart() bool {
	return c.Service.Autostart == nil || *c.Service.Autostart
}

const serviceHeader = `[service]
# Whether ` + "`shiplino setup`" + ` registers the daemon to start at login. false
# means you run ` + "`shiplino daemon`" + ` yourself, so doctor doesn't count a
# stopped daemon as an error. Rewritten by ` + "`shiplino setup [--no-service]`" + `.
`

// SetAutostart records whether setup registered the daemon, keeping the
// rest of config.toml as it was.
func SetAutostart(home string, on bool) error {
	if _, err := Load(home); err != nil {
		return err
	}
	return rewriteSection(home, "service", fmt.Sprintf("%sautostart = %t\n", serviceHeader, on))
}

// Update controls update checks (docs/updating.md). Checking asks
// api.github.com for the release list, so it's off by default;
// `shiplino update` always works on demand.
type Update struct {
	// Check looks for a new release once a day and shows it in doctor,
	// `shiplino status` and Settings. Nothing is installed.
	Check bool `toml:"check"`
	// AutoInstall also installs it (verified, like `shiplino update`) and
	// restarts the daemon. It implies Check.
	AutoInstall bool `toml:"auto_install"`
	// Channel is "stable", "prerelease", or "" to follow the installed
	// version (a prerelease build gets prereleases).
	Channel string `toml:"channel"`
	// RequireSignature refuses updates when cosign isn't installed to
	// verify the Sigstore signature (the checksum is always verified).
	RequireSignature bool `toml:"require_signature"`
}

// UpdateChecks reports whether the daemon checks for updates.
func (c Config) UpdateChecks() bool { return c.Update.Check || c.Update.AutoInstall }

// Integrations are opt-in connections to outside services.
type Integrations struct {
	GitHub GitHub `toml:"github"`
}

// GitHub shows pull request state on cards. It calls api.github.com with
// the GitHub CLI's token (or GH_TOKEN / GITHUB_TOKEN), so it's off by default.
type GitHub struct {
	Enabled bool   `toml:"enabled"`
	Poll    string `toml:"poll"` // how often open PRs are checked, e.g. "5m"
}

// GitHubPoll returns the poll interval (5 minutes by default, at least 1).
func (c Config) GitHubPoll() time.Duration {
	d, err := time.ParseDuration(c.Integrations.GitHub.Poll)
	if err != nil || d < time.Minute {
		return 5 * time.Minute
	}
	return d
}

// Budget sets spend limits (USD at list prices; 0 means none) and the
// daily digest time ("18:00", local; "" is off).
type Budget struct {
	DailyUSD   float64            `toml:"daily_usd"`
	MonthlyUSD float64            `toml:"monthly_usd"`
	Digest     string             `toml:"digest"`
	Projects   map[string]float64 `toml:"projects"`
}

// Notify controls notifications. Unset fields use the defaults
// (everything on, finished after 30s). Enabled turns desktop
// notifications off; Waiting, Finished and Failed apply to push targets
// too.
type Notify struct {
	Enabled  *bool  `toml:"enabled"`
	Waiting  *bool  `toml:"waiting"`
	Finished *bool  `toml:"finished"`
	Failed   *bool  `toml:"failed"`
	MinTurn  string `toml:"min_turn"` // e.g. "30s", "2m"
	// LimitPercent notifies when an agent reports this much of a plan
	// usage window used (default 80; 0 = off).
	LimitPercent *float64 `toml:"limit_percent"`
	Push         Push     `toml:"push"`
}

// Limits says which agents run on a flat-rate plan.
type Limits struct {
	// Plans is "plan" or "api" per agent name. Unlisted agents count as
	// on a plan once they report a usage limit.
	Plans map[string]string `toml:"plans"`
}

// LimitPercent returns the plan usage alert threshold; 0 when off.
func (c Config) LimitPercent() float64 {
	if _, on := c.NotifySettings(); !on && len(c.Notify.Push.Targets) == 0 {
		return 0
	}
	if c.Notify.LimitPercent == nil {
		return 80
	}
	return *c.Notify.LimitPercent
}

// Redaction holds user-defined patterns on top of the built-in rules.
type Redaction struct {
	ExtraPatterns []string `toml:"extra_patterns"`
}

// Path returns the config file path for a Shiplino home.
func Path(home string) string { return filepath.Join(home, "config.toml") }

// Load reads the config; a missing file means defaults. Unknown keys are
// errors, so typos don't silently do nothing.
func Load(home string) (Config, error) {
	var c Config
	b, err := os.ReadFile(Path(home))
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	dec := toml.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return c, fmt.Errorf("%s: %w", Path(home), err)
	}
	if err := validate(c); err != nil {
		return c, fmt.Errorf("%s: %w", Path(home), err)
	}
	return c, nil
}

// validate checks values the TOML types can't.
func validate(c Config) error {
	if _, err := redact.ParseLevel(c.CaptureLevel); err != nil {
		return err
	}
	if c.Budget.DailyUSD < 0 || c.Budget.MonthlyUSD < 0 {
		return errors.New("budget amounts can't be negative")
	}
	for k, v := range c.Budget.Projects {
		if v <= 0 {
			return fmt.Errorf("budget.projects.%s must be more than 0", k)
		}
	}
	if c.Budget.Digest != "" {
		if _, err := time.Parse("15:04", c.Budget.Digest); err != nil {
			return fmt.Errorf("budget.digest: want a time like \"18:00\"")
		}
	}
	if c.Notify.MinTurn != "" {
		if d, err := time.ParseDuration(c.Notify.MinTurn); err != nil || d < 0 {
			return fmt.Errorf("notify.min_turn: want a duration like \"30s\" or \"2m\"")
		}
	}
	if p := c.Notify.LimitPercent; p != nil && (*p < 0 || *p > 100) {
		return errors.New("notify.limit_percent must be 0-100")
	}
	for k, v := range c.Limits.Plans {
		if v != "plan" && v != "api" {
			return fmt.Errorf("limits.plans.%s: want \"plan\" or \"api\"", k)
		}
	}
	switch c.Update.Channel {
	case "", "stable", "prerelease":
	default:
		return fmt.Errorf("update.channel: want \"stable\" or \"prerelease\" (or leave it empty)")
	}
	if _, err := redact.New(c.Redaction.ExtraPatterns); err != nil {
		return fmt.Errorf("redaction.extra_patterns: %w", err)
	}
	if err := validateSync(c.Sync); err != nil {
		return err
	}
	return validatePush(c.Notify.Push)
}

// Level returns the capture level (Standard by default).
func (c Config) Level() redact.Level {
	l, _ := redact.ParseLevel(c.CaptureLevel)
	return l
}

// NotifySettings returns the notification settings, and whether
// notifications are on at all.
func (c Config) NotifySettings() (notify.Settings, bool) {
	s := notify.Defaults
	on := func(p *bool, def bool) bool {
		if p == nil {
			return def
		}
		return *p
	}
	s.Waiting, s.Finished, s.Failed = on(c.Notify.Waiting, s.Waiting), on(c.Notify.Finished, s.Finished), on(c.Notify.Failed, s.Failed)
	if d, err := time.ParseDuration(c.Notify.MinTurn); err == nil {
		s.MinTurn = d
	}
	return s, on(c.Notify.Enabled, true)
}

// Redactor builds the redactor for this config.
func (c Config) Redactor() *redact.Redactor {
	r, err := redact.New(c.Redaction.ExtraPatterns)
	if err != nil {
		return redact.Default
	}
	return r
}

const defaultFile = `# Shiplino settings. Changes apply when the daemon restarts.

# How much content is recorded:
#   minimal  - timing, tool names, file paths, exit codes, tokens and cost.
#              No prompts, commands, messages or outputs.
#   standard - also prompts (truncated), commands and short summaries (default)
#   full     - everything Shiplino captures, including the diff of each file
#              edit (up to 64 KB; never for secret files such as .env)
# Secrets are redacted at every level before anything is stored.
capture_level = "standard"

[redaction]
# Extra regular expressions to redact, e.g. internal ticket ids or hostnames.
extra_patterns = []

[notify]
# Desktop notifications. Test them with: shiplino notify test
enabled = true
waiting = true      # an agent is waiting on you (after 3s, so quick answers don't notify)
finished = true     # a turn finished...
min_turn = "30s"    # ...that ran at least this long
failed = true       # a session failed
limit_percent = 80  # an agent reports this much of a plan usage window used (0 = off)

` + pushHeader + `targets = []
events = []

[budget]
# Spend limits in USD at list prices (0 = none). You're notified at 80%
# and when a budget is reached. Spend counts on the day a session started.
daily_usd = 0
monthly_usd = 0
# A summary of the day's agent work at this local time, e.g. "18:00" ("" = off).
digest = ""

[budget.projects]
# Daily limits per project, by name or id, e.g.:
# api = 20

[limits.plans]
# Which agents run on a flat-rate plan ("plan") or pay per token ("api").
# An agent counts as on a plan once it reports a usage limit (Codex does on
# ChatGPT plans). For a plan agent that reports no percentages, Shiplino
# shows the tokens used in its current windows, as an estimate. E.g.:
# claude-code = "plan"

[integrations.github]
# Show pull request state (open, merged, CI checks, reviews) on cards.
# Calls api.github.com with your GitHub CLI login (or GH_TOKEN), so it's off
# by default. Only PRs of recent sessions are checked.
enabled = false
poll = "5m"

` + serviceHeader + `autostart = true

[update]
# Look for a new Shiplino release once a day and show it in doctor,
# ` + "`shiplino status`" + ` and Settings. Asks api.github.com, so it's off by
# default; ` + "`shiplino update`" + ` checks and installs on demand either way.
check = false
# Also install it (checksum and signature verified) and restart the daemon.
auto_install = false
# "stable", "prerelease", or "" to follow the installed version.
channel = ""
# Refuse updates unless cosign is installed to verify the signature.
require_signature = false

[git]
# Let ` + "`shiplino git notes`" + ` attach each recorded commit's line authorship
# (lines by agents / by others / unknown) as a git note in refs/notes/shiplino.
# Notes stay local: Shiplino never pushes them.
notes = false

` + syncHeader + `enabled = false
projects = []
exclude = []
send_titles = false
`

// WriteDefault creates a commented config file if none exists.
func WriteDefault(home string) error {
	if _, err := os.Stat(Path(home)); err == nil {
		return nil
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	return os.WriteFile(Path(home), []byte(defaultFile), 0o600)
}
