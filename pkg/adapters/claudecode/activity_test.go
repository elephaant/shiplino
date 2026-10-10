package claudecode

import (
	"bufio"
	"os"
	"testing"
	"time"

	"github.com/elephaant/shiplino/pkg/adapters"
	"github.com/elephaant/shiplino/pkg/model"
)

func parseActivity(t *testing.T, warmupLines int) []model.Event {
	t.Helper()
	f, err := os.Open("testdata/2.1/activity.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st := map[string]string{}
	var out []model.Event
	sc := bufio.NewScanner(f)
	for i := 0; sc.Scan(); i++ {
		evs, err := Adapter{}.ParseTranscriptLine(sc.Bytes(), adapters.TranscriptMeta{ReceivedAt: time.Now(), State: st, Warmup: i < warmupLines})
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range evs {
			if err := e.Validate(); err != nil {
				t.Fatal(err)
			}
		}
		out = append(out, evs...)
	}
	return out
}

func TestTranscriptActivity(t *testing.T) {
	by := map[string]model.Event{}
	kinds := map[model.Kind]int{}
	for _, e := range parseActivity(t, 0) {
		if e.Kind != model.KindUsage {
			by[e.DedupKey] = e
			kinds[e.Kind]++
			if e.Collector != model.CollectorTranscript {
				t.Errorf("%s: collector %s", e.DedupKey, e.Collector)
			}
		}
	}
	if kinds[model.KindTurnStart] != 1 || kinds[model.KindTurnEnd] != 1 || kinds[model.KindToolStart] != 3 || kinds[model.KindToolEnd] != 2 {
		t.Fatalf("kinds: %v", kinds)
	}
	// Same keys as the hooks would produce, so both sources never double count.
	sh := by["claude-code:sess-9:toolu_b1:shell"]
	if sh.Data["command"] != "npm test" || sh.Data["exit_code"] != 2 {
		t.Fatalf("shell: %+v", sh.Data)
	}
	if end := by["claude-code:sess-9:toolu_b1:end"]; end.Data["ok"] != false {
		t.Fatalf("failed tool: %+v", end.Data)
	}
	fe := by["claude-code:sess-9:toolu_e1:file"]
	if fe.Data["lines_added"] != 2 || fe.Data["lines_removed"] != 1 || fe.Data["lines_source"] != "agent" {
		t.Fatalf("edit uses Claude Code's own diff: %+v", fe.Data)
	}
	if sub := by["claude-code:sess-9:toolu_r1:start"]; sub.ActorID != "claude-code:sess-9/sub:ag-1" {
		t.Fatalf("subagent tool: %+v", sub)
	}
	if pr := by["claude-code:sess-9:pr:42"]; pr.Kind != model.KindGitPR || pr.Data["url"] != "https://github.com/acme/shop/pull/42" {
		t.Fatalf("pr-link: %+v", pr)
	}
	if ts := by["claude-code:sess-9:turn.start:prompt-u1"]; ts.Data["prompt"] != "Fix the cart rounding" {
		t.Fatalf("turn start: %+v", ts)
	}
	if te := by["claude-code:sess-9:turn.end:stop-msg_3"]; te.Data["assistant_summary"] != "Fixed the rounding." {
		t.Fatalf("turn end: %+v", te.Data)
	}
}

// After a restart the parser resumes mid-file: warmup rebuilds the
// pending tool calls, so a result read later still pairs with its call.
func TestTranscriptActivityWarmup(t *testing.T) {
	var ends int
	for _, e := range parseActivity(t, 4) { // the Bash call was in the warmup part
		if e.Kind == model.KindToolEnd && e.Data["tool_call_id"] == "toolu_b1" {
			ends++
		}
	}
	if ends != 1 {
		t.Fatalf("tool result after warmup: %d ends", ends)
	}
}
