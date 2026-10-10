// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

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
	CaptureLevel string    `toml:"capture_level"`
	Redaction    Redaction `toml:"redaction"`
	Notify       Notify    `toml:"notify"`
	Sync         Sync      `toml:"sync"`
	Budget       Budget    `toml:"budget"`
}

// Budget sets spend limits (USD at list prices; 0 means none) and the
// daily digest time ("18:00", local; "" is off).
type Budget struct {
	DailyUSD   float64            `toml:"daily_usd"`
	MonthlyUSD float64            `toml:"monthly_usd"`
	Digest     string             `toml:"digest"`
	Projects   map[string]float64 `toml:"projects"`
}

// Notify controls desktop notifications. Unset fields use the defaults
// (everything on, finished after 30s).
type Notify struct {
	Enabled  *bool  `toml:"enabled"`
	Waiting  *bool  `toml:"waiting"`
	Finished *bool  `toml:"finished"`
	Failed   *bool  `toml:"failed"`
	MinTurn  string `toml:"min_turn"` // e.g. "30s", "2m"
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
	if _, err := redact.ParseLevel(c.CaptureLevel); err != nil {
		return c, fmt.Errorf("%s: %w", Path(home), err)
	}
	if c.Budget.DailyUSD < 0 || c.Budget.MonthlyUSD < 0 {
		return c, fmt.Errorf("%s: budget amounts can't be negative", Path(home))
	}
	for k, v := range c.Budget.Projects {
		if v <= 0 {
			return c, fmt.Errorf("%s: budget.projects.%s must be more than 0", Path(home), k)
		}
	}
	if c.Budget.Digest != "" {
		if _, err := time.Parse("15:04", c.Budget.Digest); err != nil {
			return c, fmt.Errorf("%s: budget.digest: want a time like \"18:00\"", Path(home))
		}
	}
	if c.Notify.MinTurn != "" {
		if d, err := time.ParseDuration(c.Notify.MinTurn); err != nil || d < 0 {
			return c, fmt.Errorf("%s: notify.min_turn: want a duration like \"30s\" or \"2m\"", Path(home))
		}
	}
	if _, err := redact.New(c.Redaction.ExtraPatterns); err != nil {
		return c, fmt.Errorf("%s: redaction.extra_patterns: %w", Path(home), err)
	}
	if err := validateSync(c.Sync); err != nil {
		return c, fmt.Errorf("%s: %w", Path(home), err)
	}
	return c, nil
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

` + syncHeader + `enabled = false
capture_level = "minimal"
projects = []
exclude = []
send_user = false
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
