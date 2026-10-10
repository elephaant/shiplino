// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package wrap

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/elephaant/shiplino/pkg/adapters"
	"github.com/elephaant/shiplino/pkg/adapters/aider"
	"github.com/elephaant/shiplino/pkg/engine"
	"github.com/elephaant/shiplino/pkg/model"
)

var update = flag.Bool("update", false, "rewrite golden files")

var t0 = time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)

const ulid = "01K7A0000000000000000000AA"

func parse(t *testing.T, event string, p Payload, at time.Time) []model.Event {
	t.Helper()
	b, _ := json.Marshal(p)
	evs, err := Adapter{}.ParseHook(b, adapters.HookMeta{EnvelopeID: "env", Event: event, ReceivedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if err := e.Validate(); err != nil {
			t.Fatalf("%s: %v", e.Kind, err)
		}
	}
	return evs
}

func intp(i int) *int { return &i }

func TestStartAndEnd(t *testing.T) {
	cases := []struct {
		name   string
		end    Payload
		status engine.Status
	}{
		{"exit 0", Payload{ExitCode: intp(0), DurationMS: 4200}, engine.StatusDone},
		{"exit 3 fails", Payload{ExitCode: intp(3)}, engine.StatusFailed},
		{"ctrl-c is not a failure", Payload{ExitCode: intp(130), Signal: "SIGINT"}, engine.StatusDone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			start := Payload{SessionID: ulid, Agent: "goose", CWD: "/home/dev/app", Command: "goose session", Title: "goose session", PID: 4242}
			evs := parse(t, EventStart, start, t0)
			c.end.SessionID, c.end.Agent = ulid, "goose"
			evs = append(evs, parse(t, EventEnd, c.end, t0.Add(5*time.Second))...)

			if evs[0].Kind != model.KindSessionStart || evs[0].SessionID != "goose:"+ulid || evs[0].Agent.Name != "goose" ||
				evs[0].Collector != model.CollectorWrap || evs[0].Project.CWD != "/home/dev/app" || evs[0].Data["command"] != "goose session" {
				t.Fatalf("start: %+v", evs[0])
			}
			last := evs[len(evs)-1]
			if last.Kind != model.KindSessionEnd || last.Data["exit_code"] != *c.end.ExitCode {
				t.Fatalf("end: %+v", last)
			}
			eng := engine.New(nil, nil)
			for _, e := range evs {
				eng.Apply(e)
			}
			s := eng.Get("goose:" + ulid)
			if s.Status != c.status || s.Title != "goose session" || s.EndedAt.IsZero() {
				t.Fatalf("session: status=%s title=%q ended=%v", s.Status, s.Title, s.EndedAt)
			}
		})
	}
}

func TestDefaultAgentAndErrors(t *testing.T) {
	evs := parse(t, EventStart, Payload{SessionID: ulid}, t0)
	if evs[0].SessionID != "wrap:"+ulid || evs[0].Agent.Name != "wrap" {
		t.Fatalf("default agent: %+v", evs[0])
	}
	if _, err := (Adapter{}).ParseHook([]byte(`{"session_id":"x"}`), adapters.HookMeta{Event: "other"}); !errors.Is(err, adapters.ErrUnknownEvent) {
		t.Fatalf("unknown event: %v", err)
	}
	if _, err := (Adapter{}).ParseHook([]byte(`{}`), adapters.HookMeta{Event: EventStart}); err == nil {
		t.Fatal("missing session id accepted")
	}
	if _, err := (Adapter{}).ParseHook([]byte(`not json`), adapters.HookMeta{Event: EventStart}); err == nil {
		t.Fatal("bad json accepted")
	}
}

// historyLines reads the Aider fixture as `shiplino wrap` does.
func historyLines(t *testing.T) []aider.Line {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "aider", "testdata", "0.86", "chat.history.md"))
	if err != nil {
		t.Fatal(err)
	}
	var out []aider.Line
	var off int64
	for {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			return out
		}
		if text := string(bytes.TrimRight(b[:i], "\r")); aider.Relevant(text) {
			out = append(out, aider.Line{Offset: off, Len: i + 1, Text: text})
		}
		off += int64(i + 1)
		b = b[i+1:]
	}
}

// The fixture as two write bursts: the first answer (with its edits and
// commit), then the next prompts and answer.
func aiderEvents(t *testing.T) []model.Event {
	lines := historyLines(t)
	split := 0
	for i, l := range lines {
		if l.Text == "#### <blank>  " {
			split = i
		}
	}
	base := Payload{SessionID: ulid, Agent: aider.Name, CWD: "/home/dev/app", Root: "/home/dev/app"}
	evs := parse(t, EventStart, Payload{SessionID: ulid, Agent: aider.Name, CWD: "/home/dev/app", Command: "aider --model sonnet"}, t0)
	first, second := base, base
	first.Lines, second.Lines = lines[:split], lines[split:]
	evs = append(evs, parse(t, EventHistory, first, t0.Add(10*time.Second))...)
	evs = append(evs, parse(t, EventHistory, second, t0.Add(40*time.Second))...)
	return append(evs, parse(t, EventEnd, Payload{SessionID: ulid, Agent: aider.Name, CWD: "/home/dev/app", ExitCode: intp(0), DurationMS: 50000}, t0.Add(50*time.Second))...)
}

func TestAiderHistoryGolden(t *testing.T) {
	evs := aiderEvents(t)
	for i := range evs {
		evs[i].ID = ""
	}
	if filepath.Separator != '/' {
		t.Skip("golden paths are Unix paths")
	}
	b, _ := json.MarshalIndent(evs, "", "  ")
	path := filepath.Join("testdata", "aider-0.86.golden.json")
	if *update {
		os.MkdirAll("testdata", 0o755)
		os.WriteFile(path, append(b, '\n'), 0o644)
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update)", err)
	}
	if !bytes.Equal(bytes.TrimSpace(want), bytes.TrimSpace(b)) {
		t.Errorf("events differ from %s; run with -update and review the diff", path)
	}
}

func TestAiderSession(t *testing.T) {
	eng := engine.New(nil, nil)
	keys := map[string]bool{}
	for _, e := range aiderEvents(t) {
		if keys[e.DedupKey] {
			t.Fatalf("duplicate dedup key %s", e.DedupKey)
		}
		keys[e.DedupKey] = true
		eng.Apply(e)
	}
	s := eng.Get("aider:" + ulid)
	if s.Agent != "aider" || s.AgentVersion != "0.86.1" || s.Model != "anthropic/claude-sonnet-4-5" {
		t.Fatalf("meta: agent=%s version=%s model=%s", s.Agent, s.AgentVersion, s.Model)
	}
	if s.Turns != 3 || s.Title != "Add a greet function to src/greet.py" {
		t.Fatalf("turns=%d title=%q", s.Turns, s.Title)
	}
	// Tokens as Aider reports them; cached prompt tokens kept apart.
	if s.InputTokens != 2100+(12000-1300-10000) || s.OutputTokens != 512+260 || s.CacheReadTokens != 10000 || s.CacheWriteTokens != 1300 {
		t.Fatalf("tokens: in=%d out=%d cr=%d cw=%d", s.InputTokens, s.OutputTokens, s.CacheReadTokens, s.CacheWriteTokens)
	}
	// Aider's own session total wins.
	if s.CostSource != "reported" || s.BestCostUSD < 0.01419 || s.BestCostUSD > 0.01421 {
		t.Fatalf("cost: %v %s", s.BestCostUSD, s.CostSource)
	}
	want := []string{filepath.Join("/home/dev/app", "src", "greet.py"), filepath.Join("/home/dev/app", "tests", "test_greet.py")}
	if len(s.Files) != 2 || s.Files[0] != want[0] || s.Files[1] != want[1] {
		t.Fatalf("files: %v", s.Files)
	}
	if s.LastGitCommitAt.IsZero() {
		t.Fatal("auto commit not seen")
	}
	// The last edit has no commit yet: the card waits for review.
	if s.Status != engine.StatusReview || s.EndedAt.IsZero() {
		t.Fatalf("status: %s", s.Status)
	}
}

func TestTurnEndsAfterUsageReport(t *testing.T) {
	lines := historyLines(t)
	// Only the first prompt: the turn is still running.
	evs := parse(t, EventHistory, Payload{SessionID: ulid, Agent: aider.Name, Lines: lines[2:4]}, t0)
	for _, e := range evs {
		if e.Kind == model.KindTurnEnd {
			t.Fatalf("turn ended without a usage report: %+v", evs)
		}
	}
	if len(evs) != 1 || evs[0].Data["prompt"] != "Add a greet function to src/greet.py\nand a test for it" {
		t.Fatalf("prompt: %+v", evs)
	}
}
