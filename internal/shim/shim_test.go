// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package shim

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/elephaant/shiplino/internal/spool"
)

func readLines(t *testing.T, path string) []spool.Envelope {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []spool.Envelope
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var e spool.Envelope
		if err := json.Unmarshal([]byte(l), &e); err != nil {
			t.Fatalf("bad line %q: %v", l, err)
		}
		out = append(out, e)
	}
	return out
}

func TestRunWritesEnvelope(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SHIPLINO_HOME", home)
	payload := `{"session_id":"abc-123","hook_event_name":"PreToolUse","tool_name":"Edit"}`
	Run([]string{"--agent", "claude-code"}, strings.NewReader(payload))

	lines := readLines(t, spool.SessionFile(spool.Dir(home), "claude-code", "abc-123"))
	if len(lines) != 1 {
		t.Fatalf("lines = %d", len(lines))
	}
	e := lines[0]
	if e.Agent != "claude-code" || e.Event != "PreToolUse" || len(e.ID) != 26 || e.TS == 0 || e.PID == 0 {
		t.Fatalf("envelope = %+v", e)
	}
	if string(e.P) != payload {
		t.Fatalf("payload = %s", e.P)
	}
}

func TestRunEventFlagWins(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SHIPLINO_HOME", home)
	Run([]string{"--agent=cursor", "--event=afterFileEdit"}, strings.NewReader(`{"conversation_id":"c1","hook_event_name":"other"}`))
	e := readLines(t, spool.SessionFile(spool.Dir(home), "cursor", "c1"))[0]
	if e.Event != "afterFileEdit" {
		t.Fatalf("event = %q", e.Event)
	}
}

func TestRunLargePayloadGoesToBlob(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SHIPLINO_HOME", home)
	big := `{"session_id":"s","tool_response":"` + strings.Repeat("x", 10000) + `"}`
	Run([]string{"--agent", "claude-code"}, strings.NewReader(big))

	path := spool.SessionFile(spool.Dir(home), "claude-code", "s")
	raw, _ := os.ReadFile(path)
	if len(raw) > spool.MaxLine {
		t.Fatalf("line is %d bytes, want ≤ %d", len(raw), spool.MaxLine)
	}
	e := readLines(t, path)[0]
	if e.B == "" || e.P != nil {
		t.Fatalf("expected blob reference, got %+v", e)
	}
	blob, err := os.ReadFile(filepath.Join(spool.Dir(home), filepath.FromSlash(e.B)))
	if err != nil || string(blob) != big {
		t.Fatalf("blob mismatch: %v", err)
	}
}

func TestRunNonJSONIsKeptAsText(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SHIPLINO_HOME", home)
	Run([]string{"--agent", "codex"}, strings.NewReader("not json"))
	entries, _ := os.ReadDir(filepath.Join(spool.Dir(home), "codex"))
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "_unknown-") {
		t.Fatalf("entries = %v", entries)
	}
	e := readLines(t, filepath.Join(spool.Dir(home), "codex", entries[0].Name()))[0]
	if e.S != "not json" {
		t.Fatalf("s = %q", e.S)
	}
}

func TestRunPaused(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SHIPLINO_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "paused"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	Run([]string{"--agent", "claude-code"}, strings.NewReader(`{"session_id":"s"}`))
	if _, err := os.Stat(spool.Dir(home)); !os.IsNotExist(err) {
		t.Fatal("paused shim wrote to the spool")
	}
}

func TestParseFlags(t *testing.T) {
	a, e := parseFlags([]string{"--unknown", "--agent", "codex", "--event=Stop", "extra"})
	if a != "codex" || e != "Stop" {
		t.Fatalf("got %q %q", a, e)
	}
}
