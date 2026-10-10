// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

package model

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func TestULIDFormatAndTime(t *testing.T) {
	now := time.UnixMilli(1791367351512) // 2026-10-07T10:22:31.512Z
	id := NewULID(now)
	if len(id) != 26 {
		t.Fatalf("len = %d, want 26", len(id))
	}
	got, err := ULIDTime(id)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(now) {
		t.Fatalf("ULIDTime = %v, want %v", got, now)
	}
}

func TestULIDMonotonic(t *testing.T) {
	now := time.Now()
	ids := make([]string, 10000)
	for i := range ids {
		ids[i] = NewULID(now) // same millisecond every time
	}
	if !sort.StringsAreSorted(ids) {
		t.Fatal("ids from one millisecond are not sorted")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("duplicate id %s", id)
		}
		seen[id] = true
	}
	// A clock that steps backwards must not break ordering.
	before := NewULID(now.Add(-time.Hour))
	if before <= ids[len(ids)-1] {
		t.Fatal("id after clock step back sorts before earlier ids")
	}
}

func TestULIDTimeErrors(t *testing.T) {
	for _, in := range []string{"", "short", "!!!!!!!!!!!!!!!!!!!!!!!!!!"} {
		if _, err := ULIDTime(in); err == nil {
			t.Errorf("ULIDTime(%q) = nil error", in)
		}
	}
}

func validEvent() Event {
	return Event{
		ID:        NewULID(time.Now()),
		V:         SchemaVersion,
		TS:        time.Now().UTC(),
		Kind:      KindToolEnd,
		Agent:     Agent{Name: "claude-code"},
		Collector: CollectorHook,
		SessionID: SessionID("claude-code", "3f2c"),
	}
}

func TestValidate(t *testing.T) {
	cases := map[string]func(*Event){
		"missing id":        func(e *Event) { e.ID = "" },
		"bad version":       func(e *Event) { e.V = 2 },
		"missing ts":        func(e *Event) { e.TS = time.Time{} },
		"unknown kind":      func(e *Event) { e.Kind = "tool.middle" },
		"missing agent":     func(e *Event) { e.Agent.Name = "" },
		"unknown collector": func(e *Event) { e.Collector = "magic" },
		"missing session":   func(e *Event) { e.SessionID = "" },
	}
	ok := validEvent()
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid event: %v", err)
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			e := validEvent()
			mutate(&e)
			if err := e.Validate(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Validate() = %v, want ErrInvalid", err)
			}
		})
	}
}

// TestSchemaAgreement keeps the Go types and schema/event.v1.json in sync.
func TestSchemaAgreement(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "schema", "event.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Required   []string `json:"required"`
		Properties struct {
			Kind struct {
				Enum []string `json:"enum"`
			} `json:"kind"`
			Collector struct {
				Enum []string `json:"enum"`
			} `json:"collector"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	if len(schema.Properties.Kind.Enum) != len(knownKinds) {
		t.Errorf("schema has %d kinds, Go has %d", len(schema.Properties.Kind.Enum), len(knownKinds))
	}
	for _, k := range schema.Properties.Kind.Enum {
		if !Kind(k).Known() {
			t.Errorf("schema kind %q missing in Go", k)
		}
	}
	for _, c := range schema.Properties.Collector.Enum {
		if !Collector(c).known() {
			t.Errorf("schema collector %q missing in Go", c)
		}
	}

	// Every required field must appear in the JSON of a minimal valid event.
	e := validEvent()
	b, _ := json.Marshal(e)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for _, f := range schema.Required {
		if _, ok := m[f]; !ok {
			t.Errorf("required field %q not emitted", f)
		}
	}
	if _, ok := m["received_at"]; ok {
		t.Error("zero received_at should be omitted")
	}
}

func TestJSONRoundTrip(t *testing.T) {
	e := validEvent()
	e.Project = &Project{CWD: "/work/api", Branch: "main"}
	e.Data = map[string]any{"tool_call_id": "toolu_1", "ok": true}
	e.DedupKey = "claude-code:3f2c:toolu_1:end"
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var back Event
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.ID != e.ID || back.Kind != e.Kind || back.Project.Branch != "main" || back.Data["tool_call_id"] != "toolu_1" {
		t.Fatalf("round trip mismatch: %+v", back)
	}
	if err := back.Validate(); err != nil {
		t.Fatal(err)
	}
}
