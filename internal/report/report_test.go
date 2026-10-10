package report

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The fixtures are dated 2026-10-09.
var (
	fixtureNow   = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	fixtureSince = fixtureNow.Add(-30 * 24 * time.Hour)
)

const fixtures = "../../pkg/adapters/"

// noGit is a project detector without repositories.
type noGit struct{}

func (noGit) CommonDir(string) string { return "" }
func (noGit) Remote(string) string    { return "" }

func write(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// writeClaude lays out the Claude Code transcript fixture as Claude Code
// does: subagent lines in the session's subagents/ folder.
func writeClaude(t *testing.T, home string) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "projects", "-home-dev-demo")
	var main, sub bytes.Buffer
	sc := bufio.NewScanner(bytes.NewReader(fixture(t, fixtures+"claudecode/testdata/2.1/transcript.jsonl")))
	for sc.Scan() {
		if strings.Contains(sc.Text(), `"agentId"`) {
			sub.WriteString(sc.Text() + "\n")
		} else {
			main.WriteString(sc.Text() + "\n")
		}
	}
	write(t, filepath.Join(dir, "sess-0001.jsonl"), main.Bytes())
	write(t, filepath.Join(dir, "sess-0001", "subagents", "agent-ag-7.jsonl"), sub.Bytes())
	write(t, filepath.Join(home, ".claude", "projects", "-home-dev-shop", "sess-9.jsonl"), fixture(t, fixtures+"claudecode/testdata/2.1/activity.jsonl"))
}

func writeCodex(t *testing.T, home string) {
	t.Helper()
	write(t, filepath.Join(home, ".codex", "sessions", "2026", "10", "09", "rollout-2026-10-09T10-00-00-th-2.jsonl"), fixture(t, fixtures+"codex/testdata/0.153/rollout.jsonl"))
}

// snapshot lists every path under root, with files' size and modification
// time. Folder times are left out: Windows updates them lazily.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			out[p] = "dir"
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		out[p] = fmt.Sprint(fi.ModTime(), fi.Mode(), fi.Size())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func collect(t *testing.T, home string, opt Options) *Report {
	t.Helper()
	opt.UserHome, opt.Git = home, noGit{}
	if opt.Since.IsZero() {
		opt.Since, opt.Now = fixtureSince, fixtureNow
	}
	r, err := Collect(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func agent(r *Report, name string) AgentUsage {
	for _, a := range r.Agents {
		if a.Agent == name {
			return a
		}
	}
	return AgentUsage{}
}

// The report folds transcripts with the daemon's adapters and engine, so
// it gets the same numbers (see internal/daemon/transcript_test.go).
func TestCollectMatchesDaemon(t *testing.T) {
	home := t.TempDir()
	writeClaude(t, home)
	writeCodex(t, home)
	before := snapshot(t, home)
	var calls int
	r := collect(t, home, Options{Progress: func(done, total int) { calls++ }})

	if r.Transcripts != 4 || calls != 4 || r.Unreadable != 0 {
		t.Fatalf("transcripts=%d progress=%d unreadable=%d", r.Transcripts, calls, r.Unreadable)
	}
	cc := agent(r, "claude-code")
	// msg_A appears twice and counts once; the subagent's tokens count too.
	if cc.Sessions != 2 || cc.Subagents != 2 || cc.InputTokens != 1017+1200 || cc.OutputTokens != 721+80 || cc.CacheReadTokens != 23147+41579 || cc.CacheWriteTokens != 18432+3000 {
		t.Fatalf("claude-code: %+v", cc.Usage)
	}
	if math.Abs(cc.ComputedCostUSD+cc.ReportedCostUSD-cc.CostUSD) > 1e-9 || cc.CostUSD <= 0 {
		t.Fatalf("cost split: %+v", cc.Usage)
	}
	if cc.Failures.ToolFailures == 0 {
		t.Fatalf("failures: %+v", cc.Failures)
	}
	if cx := agent(r, "codex"); cx.Sessions != 1 || cx.OutputTokens == 0 {
		t.Fatalf("codex: %+v", cx.Usage)
	}
	if r.Totals.Sessions != 3 {
		t.Fatalf("totals: %+v", r.Totals)
	}
	models := map[string]bool{}
	for _, m := range r.Models {
		models[m.Model] = true
	}
	if !models["claude-opus-5-5"] || !models["claude-haiku-5-5"] {
		t.Fatalf("models: %+v", r.Models)
	}
	names := map[string]bool{}
	for _, p := range r.Projects {
		names[p.Name] = true
	}
	if !names["demo"] || !names["site"] {
		t.Fatalf("projects: %+v", r.Projects)
	}
	// Sorted by agent, then window length.
	if len(r.Limits) != 3 || !r.Limits[0].LimitReached || r.Limits[2].Agent != "codex" || r.Limits[2].Window != "7d" || r.Limits[2].PlanType != "plus" || r.Limits[2].UsedPercent == nil {
		t.Fatalf("limits: %+v", r.Limits)
	}
	// Reading changes nothing on disk.
	after := snapshot(t, home)
	if len(after) != len(before) {
		t.Fatalf("files changed: %d before, %d after", len(before), len(after))
	}
	for p, v := range before {
		if after[p] != v {
			t.Fatalf("%s changed", p)
		}
	}
}

func TestCollectFilters(t *testing.T) {
	home := t.TempDir()
	writeClaude(t, home)
	writeCodex(t, home)
	tests := []struct {
		name     string
		opt      Options
		sessions int
		agents   int
	}{
		{"agent", Options{Agent: "codex"}, 1, 1},
		{"project by name", Options{Project: "DEMO"}, 1, 1},
		{"project by id", Options{Project: "dir:/home/dev/site"}, 1, 1},
		{"unknown project", Options{Project: "nope"}, 0, 0},
		// Sessions that started before the window are left out.
		{"since", Options{Since: fixtureNow, Now: fixtureNow.Add(time.Hour)}, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := collect(t, home, tt.opt)
			if r.Totals.Sessions != tt.sessions || len(r.Agents) != tt.agents {
				t.Fatalf("sessions=%d agents=%+v", r.Totals.Sessions, r.Agents)
			}
		})
	}
}

// Old files are not read at all; broken and over-long lines are counted,
// and an unfinished last line is ignored.
func TestCollectTolerant(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".claude", "projects", "-home-dev-x")
	ok := `{"type":"assistant","sessionId":"s1","cwd":"/home/dev/x","timestamp":"2026-10-09T10:00:00.000Z","message":{"id":"m1","model":"claude-opus-5-5","usage":{"input_tokens":7,"output_tokens":3}}}`
	half := `{"type":"assistant","sessionId":"s1","timestamp":"2026-10-09T10:00:01.000Z","message":{"id":"m2","model":"claude-opus-5-5","usage":{"input_tokens":100`
	write(t, filepath.Join(dir, "s1.jsonl"), []byte("not json\n"+`{"pad":"`+strings.Repeat("x", maxLine)+`"}`+"\n"+ok+"\n"+half))
	old := filepath.Join(dir, "old.jsonl")
	write(t, old, []byte(strings.ReplaceAll(ok, "s1", "old")+"\n"))
	os.Chtimes(old, fixtureSince.Add(-time.Hour), fixtureSince.Add(-time.Hour))

	r := collect(t, home, Options{})
	if r.Transcripts != 1 || r.Lines != 3 || r.BadLines != 2 {
		t.Fatalf("transcripts=%d lines=%d bad=%d", r.Transcripts, r.Lines, r.BadLines)
	}
	if r.Totals.Sessions != 1 || r.Totals.InputTokens != 7 || r.Totals.OutputTokens != 3 {
		t.Fatalf("totals: %+v", r.Totals)
	}
}

// The text and JSON forms carry metadata only: no prompt, reply or
// command text from the transcripts.
func TestOutputHasNoContent(t *testing.T) {
	home := t.TempDir()
	writeClaude(t, home)
	writeCodex(t, home)
	r := collect(t, home, Options{})
	var text bytes.Buffer
	WriteText(&text, r)
	js, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text.String(), "claude-code") || !strings.HasSuffix(text.String(), Hint+"\n") {
		t.Fatalf("text:\n%s", text.String())
	}
	for _, secret := range []string{"fix the failing greeting test", "Fixed.", "greet"} {
		if strings.Contains(text.String(), secret) || strings.Contains(string(js), secret) {
			t.Fatalf("output contains transcript text %q", secret)
		}
	}
}

func TestTextEmpty(t *testing.T) {
	r := collect(t, t.TempDir(), Options{})
	var b bytes.Buffer
	WriteText(&b, r)
	if !strings.Contains(b.String(), "No agent sessions found") || !strings.Contains(b.String(), Hint) {
		t.Fatalf("text:\n%s", b.String())
	}
}
