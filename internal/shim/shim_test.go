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

func TestRunCopilotSessionID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SHIPLINO_HOME", home)
	Run([]string{"--agent", "copilot-cli", "--event", "sessionStart"}, strings.NewReader(`{"sessionId":"cs-1","timestamp":1,"cwd":"/w"}`))
	e := readLines(t, spool.SessionFile(spool.Dir(home), "copilot-cli", "cs-1"))[0]
	if e.Event != "sessionStart" {
		t.Fatalf("event = %q", e.Event)
	}
}

func TestMinimalStripsCopilotContent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SHIPLINO_HOME", home)
	os.WriteFile(filepath.Join(home, spool.MinimalMarker), nil, 0o600)
	for _, p := range []string{
		`{"sessionId":"c1","toolName":"bash","toolArgs":"{\"command\":\"cat notes.txt\",\"path\":\"/app/x.go\"}","toolResult":{"resultType":"success","textResultForLlm":"private output"}}`,
		`{"sessionId":"c1","toolName":"edit","toolArgs":{"path":"/app/x.go","old_str":"secret plan","new_str":"x"}}`,
		`{"sessionId":"c1","toolName":"apply_patch","toolArgs":"*** Begin Patch\n+secret plan"}`,
		`{"sessionId":"c1","agentId":"a","response":"private output","initialPrompt":"secret plan"}`,
	} {
		Run([]string{"--agent", "copilot-cli"}, strings.NewReader(p))
	}
	raw, err := os.ReadFile(spool.SessionFile(spool.Dir(home), "copilot-cli", "c1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"secret plan", "cat notes.txt", "private output"} {
		if strings.Contains(string(raw), leaked) {
			t.Errorf("%q reached the spool at minimal level", leaked)
		}
	}
	if !strings.Contains(string(raw), `/app/x.go`) {
		t.Errorf("file path dropped: %s", raw)
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

func TestMinimalStripsContentBeforeDisk(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SHIPLINO_HOME", home)
	os.WriteFile(filepath.Join(home, spool.MinimalMarker), nil, 0o600)
	payload := `{"session_id":"m1","hook_event_name":"PostToolUse","prompt":"my secret plan","tool_name":"Bash",` +
		`"tool_input":{"command":"cat notes.txt","file_path":"/app/x.go"},"tool_response":{"stdout":"private output"},"tool_use_id":"t1"}`
	Run([]string{"--agent", "claude-code"}, strings.NewReader(payload))
	raw, err := os.ReadFile(spool.SessionFile(spool.Dir(home), "claude-code", "m1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"secret plan", "cat notes.txt", "private output"} {
		if strings.Contains(string(raw), leaked) {
			t.Errorf("%q reached the spool at minimal level", leaked)
		}
	}
	for _, kept := range []string{`"session_id":"m1"`, `"tool_name":"Bash"`, `"file_path":"/app/x.go"`, `"tool_use_id":"t1"`} {
		if !strings.Contains(string(raw), strings.ReplaceAll(kept, `"`, `\"`)) && !strings.Contains(string(raw), kept) {
			t.Errorf("%s missing: %s", kept, raw)
		}
	}
}

func TestRunWindsurfPayload(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SHIPLINO_HOME", home)
	Run([]string{"--agent", "windsurf"}, strings.NewReader(`{"agent_action_name":"post_read_code","trajectory_id":"tr-1","tool_info":{"file_path":"/app/x.go"}}`))
	e := readLines(t, spool.SessionFile(spool.Dir(home), "windsurf", "tr-1"))[0]
	if e.Event != "post_read_code" {
		t.Fatalf("event = %q", e.Event)
	}
}

func TestMinimalStripsWindsurfToolInfo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SHIPLINO_HOME", home)
	os.WriteFile(filepath.Join(home, spool.MinimalMarker), nil, 0o600)
	for _, p := range []string{
		`{"agent_action_name":"pre_user_prompt","trajectory_id":"w1","tool_info":{"user_prompt":"my secret plan"}}`,
		`{"agent_action_name":"post_cascade_response","trajectory_id":"w1","tool_info":{"response":"private output"}}`,
		`{"agent_action_name":"post_write_code","trajectory_id":"w1","tool_info":{"file_path":"/app/x.go","edits":[{"old_string":"a","new_string":"private edit"}]}}`,
		`{"agent_action_name":"post_run_command","trajectory_id":"w1","tool_info":{"command_line":"cat notes.txt","cwd":"/app"}}`,
		`{"agent_action_name":"post_mcp_tool_use","trajectory_id":"w1","tool_info":{"mcp_server_name":"github","mcp_tool_name":"create_issue","mcp_tool_arguments":{"body":"private body"},"mcp_result":"private result"}}`,
	} {
		Run([]string{"--agent", "windsurf"}, strings.NewReader(p))
	}
	raw, err := os.ReadFile(spool.SessionFile(spool.Dir(home), "windsurf", "w1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"secret plan", "private output", "private edit", "cat notes.txt", "private body", "private result"} {
		if strings.Contains(string(raw), leaked) {
			t.Errorf("%q reached the spool at minimal level", leaked)
		}
	}
	for _, kept := range []string{"/app/x.go", "cwd", "github", "create_issue"} {
		if !strings.Contains(string(raw), kept) {
			t.Errorf("%s missing: %s", kept, raw)
		}
	}
}

func TestMinimalStripsGeminiCLIContent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SHIPLINO_HOME", home)
	os.WriteFile(filepath.Join(home, spool.MinimalMarker), nil, 0o600)
	for _, payload := range []string{
		`{"session_id":"g1","hook_event_name":"AfterAgent","prompt":"my secret plan","prompt_response":"private answer"}`,
		`{"session_id":"g1","hook_event_name":"Notification","notification_type":"ToolPermission","message":"Approve cat notes.txt","details":{"command":"cat notes.txt"}}`,
		`{"session_id":"g1","hook_event_name":"AfterTool","tool_name":"list_directory","tool_input":{"dir_path":"/app/src","ignore":["secret-dir"]},"tool_response":{"llmContent":"private listing"}}`,
	} {
		Run([]string{"--agent", "gemini-cli"}, strings.NewReader(payload))
	}
	raw, err := os.ReadFile(spool.SessionFile(spool.Dir(home), "gemini-cli", "g1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"secret plan", "private answer", "cat notes.txt", "secret-dir", "private listing"} {
		if strings.Contains(string(raw), leaked) {
			t.Errorf("%q reached the spool at minimal level", leaked)
		}
	}
	if !strings.Contains(string(raw), "/app/src") || !strings.Contains(string(raw), "ToolPermission") {
		t.Errorf("paths and event types must survive: %s", raw)
	}
}
