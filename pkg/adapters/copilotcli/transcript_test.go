package copilotcli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/elephaant/shiplino/pkg/adapters"
	"github.com/elephaant/shiplino/pkg/model"
)

const logSession = "3f0c9a52-7d1e-4b8a-9c11-2a6e5f4d8b70"

var logPath = filepath.Join("testdata", fixtures, "session-state", logSession, "events.jsonl")

// parseLog parses the session log fixture's lines, the first warm of
// them with Warmup set (a daemon restart), all with one State.
func parseLog(t *testing.T, warm int) []model.Event {
	t.Helper()
	f, err := os.Open(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	state := map[string]string{}
	var all []model.Event
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for i := 0; sc.Scan(); i++ {
		evs, err := Adapter{}.ParseTranscriptLine(sc.Bytes(), adapters.TranscriptMeta{ReceivedAt: t0, Path: logPath, State: state, Warmup: i < warm})
		if err != nil {
			t.Fatalf("line %d: %v", i+1, err)
		}
		if i < warm && len(evs) > 0 {
			t.Fatalf("line %d: events during warmup", i+1)
		}
		for _, e := range evs {
			if err := e.Validate(); err != nil {
				t.Fatalf("line %d: %v", i+1, err)
			}
		}
		all = append(all, evs...)
	}
	return all
}

func TestSessionLogGolden(t *testing.T) {
	all := parseLog(t, 0)
	for i := range all {
		all[i].ID, all[i].ReceivedAt = "", t0
	}
	b, _ := json.MarshalIndent(all, "", "  ")
	path := filepath.Join("testdata", fixtures, "usage.golden.json")
	if *update {
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

func TestSessionLogUsage(t *testing.T) {
	all := parseLog(t, 0)
	// First shutdown: two models. Second: only what changed since.
	if len(all) != 3 {
		t.Fatalf("%d usage events, want 3", len(all))
	}
	claude, mini, more := all[0], all[1], all[2]
	if claude.SessionID != "copilot-cli:"+logSession || claude.Data["model"] != "claude-sonnet-4.5" ||
		claude.Data["input_tokens"] != int64(1000) || claude.Data["cache_read_tokens"] != int64(9000) || claude.Data["output_tokens"] != int64(800) {
		t.Fatalf("first shutdown: %v", claude.Data)
	}
	// Copilot's AI credits: 4.2 credits at $0.01.
	if claude.Data["cost_source"] != "reported" || claude.Data["cost_usd"].(float64) < 0.04199 || claude.Data["cost_usd"].(float64) > 0.04201 {
		t.Fatalf("first shutdown cost: %v", claude.Data)
	}
	if claude.Project == nil || claude.Project.CWD != "/home/dev/shop" || claude.Agent.Version != "1.0.95" {
		t.Fatalf("project/version: %+v %+v", claude.Project, claude.Agent)
	}
	if mini.Data["model"] != "gpt-5-mini" || mini.Data["cost_source"] != "unpriced" {
		t.Fatalf("model without credits or price: %v", mini.Data)
	}
	if more.Data["input_tokens"] != int64(1000) || more.Data["output_tokens"] != int64(700) || more.Data["cache_read_tokens"] != int64(6000) ||
		more.Data["reasoning_tokens"] != int64(50) || more.Project.Branch != "cart-test" || more.DedupKey != "copilot-cli:"+logSession+":usage:e-10:claude-sonnet-4.5" {
		t.Fatalf("second shutdown: %v %+v %s", more.Data, more.Project, more.DedupKey)
	}

	// After a restart, the warmup rebuilds the totals: the same difference.
	again := parseLog(t, 9)
	if len(again) != 1 || again[0].DedupKey != more.DedupKey || again[0].Data["output_tokens"] != int64(700) {
		t.Fatalf("after warmup: %+v", again)
	}
}

func TestSessionLogReset(t *testing.T) {
	state := map[string]string{}
	parse := func(id, metrics string) []model.Event {
		line := `{"type":"session.shutdown","id":"` + id + `","timestamp":"2026-10-10T08:00:00Z","data":{"modelMetrics":{"claude-opus-4.7-1m":` + metrics + `}}}`
		evs, err := Adapter{}.ParseTranscriptLine([]byte(line), adapters.TranscriptMeta{ReceivedAt: t0, Path: logPath, State: state})
		if err != nil {
			t.Fatal(err)
		}
		return evs
	}
	parse("a", `{"usage":{"inputTokens":5000,"outputTokens":300}}`)
	// An older Copilot restarted its totals on resume: count them whole.
	evs := parse("b", `{"usage":{"inputTokens":2000,"outputTokens":100}}`)
	if len(evs) != 1 || evs[0].Data["input_tokens"] != int64(2000) || evs[0].Data["output_tokens"] != int64(100) {
		t.Fatalf("reset: %+v", evs)
	}
	// Priced from the table under its own id when Copilot has no credits.
	if evs[0].Data["cost_source"] != "computed" || evs[0].Data["model"] != "claude-opus-4.7-1m" {
		t.Fatalf("pricing: %v", evs[0].Data)
	}
	if evs := parse("c", `{"usage":{"inputTokens":2000,"outputTokens":100}}`); len(evs) != 0 {
		t.Fatalf("unchanged totals: %+v", evs)
	}
}

func TestPriceID(t *testing.T) {
	for in, want := range map[string]string{
		"claude-sonnet-4.5":           "claude-sonnet-4-5",
		"claude-opus-4.6-1m":          "claude-opus-4-6",
		"claude-opus-4.7-1m-internal": "claude-opus-4-7",
		"gpt-5.5":                     "gpt-5.5",
	} {
		if got := priceID(in); got != want {
			t.Errorf("priceID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSessionLogRoots(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COPILOT_HOME", "")
	if r := (Adapter{}).TranscriptRoots(home, t0, t0); r != nil {
		t.Fatalf("no session-state yet: %v", r)
	}
	dir := filepath.Join(home, ".copilot", "session-state", "s1")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "events.jsonl"), nil, 0o600)
	r := Adapter{}.TranscriptRoots(home, t0, t0)
	if m, _ := filepath.Glob(r[0]); len(m) != 1 {
		t.Fatalf("roots %v find %v", r, m)
	}
}

func TestSessionLogHasNoContent(t *testing.T) {
	for _, e := range parseLog(t, 0) {
		b, _ := json.Marshal(e)
		for _, s := range []string{"cart total", "discounts", "Added the test", "cart.test.ts"} {
			if bytes.Contains(b, []byte(s)) {
				t.Fatalf("usage event carries content %q", s)
			}
		}
	}
}
