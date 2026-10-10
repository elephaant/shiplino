// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package config

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/elephaant/shiplino/pkg/redact"
)

func TestDefaultsAndRoundTrip(t *testing.T) {
	home := t.TempDir()
	c, err := Load(home)
	if err != nil || c.Level() != redact.Standard {
		t.Fatalf("missing file: %+v %v", c, err)
	}
	if err := WriteDefault(home); err != nil {
		t.Fatal(err)
	}
	c, err = Load(home)
	if err != nil || c.Level() != redact.Standard {
		t.Fatalf("default file: %+v %v", c, err)
	}
	os.WriteFile(Path(home), []byte("capture_level = \"minimal\"\n[redaction]\nextra_patterns = [\"ACME-[0-9]+\"]\n"), 0o600)
	c, err = Load(home)
	if err != nil || c.Level() != redact.Minimal || c.Redactor().Text("ACME-42") != "«redacted:custom»" {
		t.Fatalf("custom: %+v %v", c, err)
	}
	if WriteDefault(home); !strings.Contains(readFile(t, Path(home)), "minimal") {
		t.Fatal("WriteDefault overwrote an existing file")
	}
}

func TestRejectsMistakes(t *testing.T) {
	for name, content := range map[string]string{
		"unknown key":  "capture_levle = \"minimal\"\n",
		"bad level":    "capture_level = \"everything\"\n",
		"bad pattern":  "[redaction]\nextra_patterns = [\"([\"]\n",
		"invalid toml": "capture_level = \n",
	} {
		home := t.TempDir()
		os.WriteFile(Path(home), []byte(content), 0o600)
		if _, err := Load(home); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func readFile(t *testing.T, p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestNotifySettings(t *testing.T) {
	home := t.TempDir()
	if err := WriteDefault(home); err != nil {
		t.Fatal(err)
	}
	c, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if s, on := c.NotifySettings(); !on || !s.Waiting || !s.Finished || !s.Failed || s.MinTurn != 30*time.Second {
		t.Fatalf("defaults: %+v %v", s, on)
	}
	os.WriteFile(Path(home), []byte("[notify]\nfinished = false\nmin_turn = \"2m\"\n"), 0o600)
	c, _ = Load(home)
	if s, on := c.NotifySettings(); !on || s.Finished || !s.Waiting || s.MinTurn != 2*time.Minute {
		t.Fatalf("custom: %+v %v", s, on)
	}
	os.WriteFile(Path(home), []byte("[notify]\nenabled = false\n"), 0o600)
	c, _ = Load(home)
	if _, on := c.NotifySettings(); on {
		t.Fatal("enabled = false ignored")
	}
	os.WriteFile(Path(home), []byte("[notify]\nmin_turn = \"soon\"\n"), 0o600)
	if _, err := Load(home); err == nil || !strings.Contains(err.Error(), "min_turn") {
		t.Fatalf("bad min_turn: %v", err)
	}
}
