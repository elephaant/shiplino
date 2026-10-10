// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

package claudecode

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/elephaant/shiplino/pkg/adapters"
	"github.com/elephaant/shiplino/pkg/model"
)

var update = flag.Bool("update", false, "rewrite golden files")

var t0 = time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)

// parseFixture feeds every payload line through the adapter with stable meta.
func parseFixture(t *testing.T, path string) []model.Event {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []model.Event
	sc := bufio.NewScanner(f)
	for i := 0; sc.Scan(); i++ {
		meta := adapters.HookMeta{EnvelopeID: fmt.Sprintf("env-%03d", i), ReceivedAt: t0.Add(time.Duration(i) * time.Second)}
		evs, err := Adapter{}.ParseHook(sc.Bytes(), meta)
		if err != nil {
			t.Fatalf("line %d: %v", i+1, err)
		}
		for _, e := range evs {
			if err := e.Validate(); err != nil {
				t.Fatalf("line %d: invalid event: %v", i+1, err)
			}
			e.ID = "" // random part of the ULID; everything else is deterministic
			out = append(out, e)
		}
	}
	return out
}

func TestGoldenSession(t *testing.T) {
	got := parseFixture(t, filepath.Join("testdata", "2.1", "session.jsonl"))
	b, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "2.1", "session.golden.json")
	if *update {
		if err := os.WriteFile(golden, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run go test -update to create it)", err)
	}
	if !bytes.Equal(bytes.TrimSpace(want), bytes.TrimSpace(b)) {
		t.Errorf("events differ from %s; run `go test ./pkg/adapters/claudecode -update` and review the diff", golden)
	}
}

func TestDedupKeysAreUniqueAndStable(t *testing.T) {
	path := filepath.Join("testdata", "2.1", "session.jsonl")
	first, second := parseFixture(t, path), parseFixture(t, path)
	seen := map[string]bool{}
	for i, e := range first {
		if e.DedupKey != second[i].DedupKey {
			t.Fatalf("dedup key not stable: %q vs %q", e.DedupKey, second[i].DedupKey)
		}
		if seen[e.DedupKey] {
			t.Fatalf("duplicate dedup key %q", e.DedupKey)
		}
		seen[e.DedupKey] = true
	}
}

func TestToolStartAndEndPairByID(t *testing.T) {
	events := parseFixture(t, filepath.Join("testdata", "2.1", "session.jsonl"))
	starts := map[string]bool{}
	for _, e := range events {
		if e.Kind == model.KindToolStart {
			starts[e.Data["tool_call_id"].(string)] = true
		}
	}
	for _, e := range events {
		if e.Kind == model.KindToolEnd && e.Data["tool_call_id"] == "toolu_03" && !starts["toolu_03"] {
			t.Fatal("toolu_03 end without start")
		}
	}
}

func TestSubagentActor(t *testing.T) {
	raw := []byte(`{"session_id":"s","hook_event_name":"PreToolUse","agent_id":"a1","agent_type":"Explore","tool_name":"Glob","tool_input":{"pattern":"**/*.go"},"tool_use_id":"t1"}`)
	evs, err := Adapter{}.ParseHook(raw, adapters.HookMeta{EnvelopeID: "e", ReceivedAt: t0})
	if err != nil {
		t.Fatal(err)
	}
	e := evs[0]
	if e.SessionID != "claude-code:s" || e.ActorID != "claude-code:s/sub:a1" || e.ParentActor != "claude-code:s" || e.ActorType != "Explore" {
		t.Fatalf("actor fields: %+v", e)
	}
	if e.Data["tool"] != model.ToolSearch || e.Data["input_summary"] != "**/*.go" {
		t.Fatalf("data: %v", e.Data)
	}
}

func TestErrors(t *testing.T) {
	if _, err := (Adapter{}).ParseHook([]byte(`{"session_id":"s","hook_event_name":"BrandNewEvent"}`), adapters.HookMeta{ReceivedAt: t0}); !errors.Is(err, adapters.ErrUnknownEvent) {
		t.Errorf("unknown event: err = %v", err)
	}
	if _, err := (Adapter{}).ParseHook([]byte(`not json`), adapters.HookMeta{ReceivedAt: t0}); err == nil {
		t.Error("bad JSON: want error")
	}
	if _, err := (Adapter{}).ParseHook([]byte(`{"hook_event_name":"Stop"}`), adapters.HookMeta{ReceivedAt: t0}); err == nil {
		t.Error("missing session_id: want error")
	}
	// The event name may come from the shim's --event flag instead of the payload.
	evs, err := Adapter{}.ParseHook([]byte(`{"session_id":"s"}`), adapters.HookMeta{Event: "Stop", ReceivedAt: t0})
	if err != nil || len(evs) != 1 || evs[0].Kind != model.KindTurnEnd {
		t.Errorf("event from meta: %v %v", evs, err)
	}
}

func TestNormalizeTool(t *testing.T) {
	cases := map[string]string{
		"Edit": "edit", "MultiEdit": "edit", "Write": "write", "Read": "read", "Bash": "shell",
		"Grep": "search", "Glob": "search", "WebFetch": "web", "WebSearch": "web", "Task": "task",
		"Agent": "task", "mcp__github__get_issue": "mcp", "TodoWrite": "other",
	}
	for in, want := range cases {
		if got := NormalizeTool(in); got != want {
			t.Errorf("NormalizeTool(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSummarizeTruncates(t *testing.T) {
	long := make([]byte, 500)
	for i := range long {
		long[i] = 'a'
	}
	in, _ := json.Marshal(map[string]string{"command": string(long)})
	if s := summarize("Bash", in); len([]rune(s)) != maxSummary {
		t.Fatalf("len = %d", len([]rune(s)))
	}
}

func TestRegistered(t *testing.T) {
	if a, ok := adapters.Get(Name); !ok || a.Name() != Name {
		t.Fatal("claude-code adapter not registered")
	}
}

// Cursor runs Claude Code hooks with its own payloads; its native hooks
// record those sessions, so these are skipped.
func TestCursorRunPayloadIsSkipped(t *testing.T) {
	raw := `{"session_id":"c1","conversation_id":"c1","hook_event_name":"PostToolUse","tool_name":"Shell","cursor_version":"2026.09.02"}`
	evs, err := Adapter{}.ParseHook([]byte(raw), adapters.HookMeta{EnvelopeID: "e", ReceivedAt: time.Now()})
	if err != nil || len(evs) != 0 {
		t.Fatalf("got %v %v", evs, err)
	}
}
