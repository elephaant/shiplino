// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package claudecode

import (
	"bufio"
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/elephaant/shiplino/pkg/adapters"
	"github.com/elephaant/shiplino/pkg/model"
)

func parseTranscriptFixture(t *testing.T) []model.Event {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "2.1", "transcript.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []model.Event
	sc := bufio.NewScanner(f)
	for i := 0; sc.Scan(); i++ {
		evs, err := Adapter{}.ParseTranscriptLine(sc.Bytes(), adapters.TranscriptMeta{ReceivedAt: t0})
		if err != nil {
			t.Fatalf("line %d: %v", i+1, err)
		}
		for _, e := range evs {
			if err := e.Validate(); err != nil {
				t.Fatalf("line %d: %v", i+1, err)
			}
			e.ID = ""
			out = append(out, e)
		}
	}
	return out
}

func TestTranscriptGolden(t *testing.T) {
	got := parseTranscriptFixture(t)
	b, _ := json.MarshalIndent(got, "", "  ")
	golden := filepath.Join("testdata", "2.1", "transcript.golden.json")
	if *update {
		os.WriteFile(golden, append(b, '\n'), 0o644)
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run go test -update)", err)
	}
	if !bytes.Equal(bytes.TrimSpace(want), bytes.TrimSpace(b)) {
		t.Errorf("events differ from %s; run with -update and review the diff", golden)
	}
}

func TestTranscriptUsage(t *testing.T) {
	evs := parseTranscriptFixture(t)

	// The split response (msg_A on two lines) yields two events with the
	// SAME dedup key, so the store counts it once. <synthetic> and non-
	// assistant lines produce nothing.
	keys := map[string]int{}
	for _, e := range evs {
		keys[e.DedupKey]++
	}
	if keys["claude-code:sess-0001:usage:msg_A"] != 2 || len(keys) != 4 {
		t.Fatalf("dedup keys: %v", keys)
	}

	byMsg := map[string]model.Event{}
	for _, e := range evs {
		byMsg[e.Data["message_id"].(string)] = e
	}
	b := byMsg["msg_B"]
	// Opus 5.5: 5 in × $4 + 420 out × $20 + 41579 read × $0.20 + 1000 5m × $5 + 2000 1h × $8, per MTok.
	want := (5*4 + 420*20 + 41579*0.2 + 1000*5 + 2000*8) / 1e6
	if math.Abs(b.Data["cost_usd"].(float64)-want) > 1e-12 {
		t.Fatalf("msg_B cost = %v, want %v", b.Data["cost_usd"], want)
	}
	if b.Agent.Version != "2.1.290" || b.Collector != model.CollectorTranscript || b.Data["cache_write_tokens"] != int64(3000) {
		t.Fatalf("msg_B: %+v", b)
	}

	s := byMsg["msg_S"]
	if s.ActorID != "claude-code:sess-0001/sub:ag-7" || s.ParentActor != "claude-code:sess-0001" {
		t.Fatalf("subagent attribution: %+v", s)
	}
	if x := byMsg["msg_X"]; x.Data["cost_source"] != "unpriced" || x.Data["cost_usd"] != nil {
		t.Fatalf("unknown model: %v", x.Data)
	}
}

func TestTranscriptBadLine(t *testing.T) {
	if _, err := (Adapter{}).ParseTranscriptLine([]byte("{nope"), adapters.TranscriptMeta{ReceivedAt: t0}); err == nil {
		t.Fatal("bad JSON accepted")
	}
}

var _ adapters.TranscriptParser = Adapter{}
