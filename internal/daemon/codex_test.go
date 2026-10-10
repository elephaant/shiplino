package daemon

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/elephaant/shiplino/internal/shim"
)

const codexFixtures = "../../pkg/adapters/codex/testdata/0.153/"

// codexRollout copies lines of the rollout fixture to where Codex keeps
// today's sessions, with the session id replaced.
func codexRollout(t *testing.T, home, id string, lines []string) string {
	t.Helper()
	now := time.Now()
	path := filepath.Join(home, ".codex", "sessions", now.Format("2006"), now.Format("01"), now.Format("02"), "rollout-"+id+".jsonl")
	os.MkdirAll(filepath.Dir(path), 0o700)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func fixtureLines(t *testing.T, name string) []string {
	t.Helper()
	f, err := os.Open(codexFixtures + name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}

// A desktop-app session has no hooks: everything comes from the rollout,
// found by discovery.
func TestCodexHooklessRollout(t *testing.T) {
	home := withHome(t)
	e := newEnv(t)
	lines := fixtureLines(t, "rollout.jsonl")
	path := codexRollout(t, home, "th-2", lines[:5]) // up to the first usage record
	e.poll()

	s := e.session("codex:th-2")
	if s.Model != "gpt-5.6-terra" || s.AgentVersion != "0.153.4" || s.Branch != "main" || s.Turns != 1 {
		t.Fatalf("session: %+v", s)
	}
	if s.InputTokens != 31527-12032 || s.CacheReadTokens != 12032 {
		t.Fatalf("tokens: %+v", s)
	}

	// Restart mid-file: the parser state (model, turn) is rebuilt by the
	// warmup pass, so new usage still carries the model.
	e.restart()
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(strings.Join(lines[5:], "\n") + "\n")
	f.Close()
	e.poll()

	s = e.session("codex:th-2")
	if s.Turns != 3 || s.ToolCalls != 3 || s.InputTokens != (31527-12032)+(32000-31000)+1000 || s.OutputTokens != 550 || s.Model != "gpt-5.6-terra-mini" {
		t.Fatalf("after restart: turns=%d tools=%d in=%d out=%d", s.Turns, s.ToolCalls, s.InputTokens, s.OutputTokens)
	}
	if s.LinesAdded != 4 || s.LinesRemoved != 1 || len(s.Files) != 2 {
		t.Fatalf("edits: +%d -%d %v", s.LinesAdded, s.LinesRemoved, s.Files)
	}
	if s.Status != "idle" && s.Status != "review" && s.Status != "done" {
		t.Fatalf("status after aborted turn: %s", s.Status)
	}
}

// A CLI session has hooks and a rollout. Rollout item ids don't match hook
// call ids, so its activity would double count: only usage is taken.
func TestCodexHooksWinOverRolloutActivity(t *testing.T) {
	home := withHome(t)
	e := newEnv(t)
	rollout := fixtureLines(t, "rollout.jsonl")
	for i := range rollout {
		rollout[i] = strings.ReplaceAll(rollout[i], "th-2", "th-1")
	}
	path := codexRollout(t, home, "th-1", rollout)
	for _, l := range fixtureLines(t, "hooks.jsonl") {
		l = strings.ReplaceAll(l, "/home/dev/.codex/sessions/2026/10/09/rollout-x.jsonl", filepath.ToSlash(path))
		shim.Run([]string{"--agent", "codex"}, strings.NewReader(l))
	}
	e.poll()

	s := e.session("codex:th-1")
	// One PreToolUse in the hooks; the rollout's three items are dropped.
	if s.ToolCalls != 1 {
		t.Fatalf("tool calls = %d, want 1 from hooks", s.ToolCalls)
	}
	if s.InputTokens != (31527-12032)+(32000-31000)+1000 {
		t.Fatalf("usage from the rollout missing: in=%d", s.InputTokens)
	}
	if !s.HookSeen {
		t.Fatal("HookSeen not set")
	}
}
