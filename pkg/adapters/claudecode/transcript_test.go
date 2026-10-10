// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

package claudecode

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/elephaant/shiplino/pkg/adapters"
	"github.com/elephaant/shiplino/pkg/model"
	"github.com/elephaant/shiplino/pkg/pricing"
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
	if keys["claude-code:usage:msg_A"] != 2 || keys["claude-code:sess-0001:cost-state:1791367200000:0.250000000"] != 2 || keys["claude-code:sess-0001:toolu_01:start"] != 1 || len(keys) != 8 {
		t.Fatalf("dedup keys: %v", keys)
	}

	byMsg := map[string]model.Event{}
	for _, e := range evs {
		if id, ok := e.Data["message_id"].(string); ok {
			byMsg[id] = e
		}
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
	// Fast mode (2x) and US inference (1.1x) stack; 3 web searches add $0.03.
	w := byMsg["msg_W"]
	if want := (1000*4+100*20)/1e6*2.2 + 0.03; math.Abs(w.Data["cost_usd"].(float64)-want) > 1e-12 {
		t.Fatalf("msg_W cost = %v, want %v", w.Data["cost_usd"], want)
	}
	var report model.Event
	for _, e := range evs {
		if e.Data["report"] == true {
			report = e
		}
	}
	if report.Data["process"] != "claude-code:1791367200000" || report.Data["total_cost_usd"] != 0.25 || report.Data["cost_source"] != "reported" {
		t.Fatalf("cost report: %v", report.Data)
	}
}

func TestTranscriptBadLine(t *testing.T) {
	if _, err := (Adapter{}).ParseTranscriptLine([]byte("{nope"), adapters.TranscriptMeta{ReceivedAt: t0}); err == nil {
		t.Fatal("bad JSON accepted")
	}
}

var _ adapters.TranscriptParser = Adapter{}

// A response written over several lines with growing counts counts once,
// at its final size; a copy in another session's file adds nothing.
func TestUsageGrowsAndCountsOnce(t *testing.T) {
	line := func(sess string, out int) []byte {
		return []byte(fmt.Sprintf(`{"type":"assistant","sessionId":%q,"timestamp":"2026-10-09T10:00:00Z","message":{"id":"msg_G","model":"claude-opus-5-5","usage":{"input_tokens":10,"output_tokens":%d,"cache_read_input_tokens":1000}}}`, sess, out))
	}
	parse := func(st map[string]string, b []byte, warm bool) []model.Event {
		evs, err := Adapter{}.ParseTranscriptLine(b, adapters.TranscriptMeta{ReceivedAt: time.Now(), State: st, Warmup: warm})
		if err != nil {
			t.Fatal(err)
		}
		return evs
	}
	st := map[string]string{}
	first := parse(st, line("s1", 5), false)
	same := parse(st, line("s1", 5), false)
	grown := parse(st, line("s1", 405), false)
	if len(first) != 1 || first[0].DedupKey != "claude-code:usage:msg_G" || first[0].Data["output_tokens"] != int64(5) {
		t.Fatalf("first: %+v", first)
	}
	if len(same) != 0 {
		t.Fatalf("unchanged line produced %+v", same)
	}
	if len(grown) != 1 || grown[0].Data["output_tokens"] != int64(400) || grown[0].Data["input_tokens"] != int64(0) || grown[0].Data["correction"] != true {
		t.Fatalf("growth: %+v", grown)
	}
	full, _ := pricing.Default().Cost("claude-opus-5-5", pricing.Usage{Input: 10, Output: 405, CacheRead: 1000})
	sum := first[0].Data["cost_usd"].(float64) + grown[0].Data["cost_usd"].(float64)
	if diff := sum - full; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("cost of the parts %v != cost of the whole %v", sum, full)
	}
	// The same response copied into a resumed session's file: same keys,
	// so the store keeps one.
	copyFirst := parse(map[string]string{}, line("s2", 405), false)
	if copyFirst[0].DedupKey != first[0].DedupKey {
		t.Fatalf("copy key %s", copyFirst[0].DedupKey)
	}
	// After a restart the warmup rebuilds the state without events.
	st2 := map[string]string{}
	if evs := parse(st2, line("s1", 5), true); len(evs) != 0 {
		t.Fatal("warmup emitted")
	}
	if evs := parse(st2, line("s1", 405), false); len(evs) != 1 || evs[0].DedupKey != grown[0].DedupKey {
		t.Fatalf("after warmup: %+v", evs)
	}
}
