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

	"github.com/pelletier/go-toml/v2"

	"github.com/elephaant/shiplino/pkg/redact"
)

// Config is the user's settings. Zero values mean defaults.
type Config struct {
	CaptureLevel string    `toml:"capture_level"`
	Redaction    Redaction `toml:"redaction"`
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
	if _, err := redact.New(c.Redaction.ExtraPatterns); err != nil {
		return c, fmt.Errorf("%s: redaction.extra_patterns: %w", Path(home), err)
	}
	return c, nil
}

// Level returns the capture level (Standard by default).
func (c Config) Level() redact.Level {
	l, _ := redact.ParseLevel(c.CaptureLevel)
	return l
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
#   full     - everything Shiplino captures
# Secrets are redacted at every level before anything is stored.
capture_level = "standard"

[redaction]
# Extra regular expressions to redact, e.g. internal ticket ids or hostnames.
extra_patterns = []
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
