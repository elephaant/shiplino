package aider

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// FixtureLines reads a history file the way `shiplino wrap` does: every
// line with its offset, keeping only relevant ones.
func fixtureLines(t *testing.T) []Line {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "0.86", "chat.history.md"))
	if err != nil {
		t.Fatal(err)
	}
	var out []Line
	var off int64
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			break
		}
		if text := string(bytes.TrimRight(b[:i], "\r")); Relevant(text) {
			out = append(out, Line{Offset: off, Len: i + 1, Text: text})
		}
		off += int64(i + 1)
		b = b[i+1:]
	}
	return out
}

func TestParseHistory(t *testing.T) {
	items := Parse(fixtureLines(t))
	type want struct {
		kind ItemKind
		text string
	}
	wants := []want{
		{ItemVersion, "0.86.1"},
		{ItemModel, "anthropic/claude-sonnet-4-5"},
		{ItemPrompt, "Add a greet function to src/greet.py\nand a test for it"},
		{ItemTokens, ""},
		{ItemCost, ""},
		{ItemEdit, "src/greet.py"},
		{ItemEdit, "tests/test_greet.py"},
		{ItemCommit, "feat: Add greet function with test"},
		{ItemPrompt, ""},
		{ItemPrompt, "rename greet to hello"},
		{ItemTokens, ""},
		{ItemCost, ""},
		{ItemEdit, "src/greet.py"},
	}
	if len(items) != len(wants) {
		t.Fatalf("got %d items, want %d: %+v", len(items), len(wants), items)
	}
	for i, w := range wants {
		if items[i].Kind != w.kind || items[i].Text != w.text {
			t.Errorf("item %d: got %s %q, want %s %q", i, items[i].Kind, items[i].Text, w.kind, w.text)
		}
	}
	if tk := items[3]; tk.Sent != 2100 || tk.Received != 512 || tk.CacheHit != 0 || tk.CacheWrite != 0 {
		t.Errorf("tokens: %+v", tk)
	}
	if c := items[4]; c.MessageCost != 0.01 || c.SessionCost != 0.01 {
		t.Errorf("cost: %+v", c)
	}
	if items[7].SHA != "1a2b3c4" {
		t.Errorf("commit sha: %q", items[7].SHA)
	}
	// Both cache figures: the Cost part moves to its own line.
	if tk := items[10]; tk.Sent != 12000 || tk.CacheWrite != 1300 || tk.CacheHit != 10000 || tk.Received != 260 {
		t.Errorf("cached tokens: %+v", tk)
	}
	if c := items[11]; c.MessageCost != 0.0042 || c.SessionCost != 0.0142 {
		t.Errorf("small cost: %+v", c)
	}
}

func TestRelevantKeepsAnswersOut(t *testing.T) {
	for _, s := range []string{"Here are the changes.", "def greet(name):", "> Git repo: .git with 12 files  ", "# aider chat started at 2026-10-10 09:00:00", "####nospace"} {
		if Relevant(s) {
			t.Errorf("%q should not be relevant", s)
		}
	}
}

func TestMinimize(t *testing.T) {
	cases := map[string]string{
		"#### fix the login bug  ":          "####",
		"> Commit 1a2b3c4 fix: login bug  ": "> Commit 1a2b3c4",
		"> Applied edit to src/a.py  ":      "> Applied edit to src/a.py",
	}
	for in, want := range cases {
		if got := Minimize(in); got != want {
			t.Errorf("Minimize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHistoryPath(t *testing.T) {
	repo := t.TempDir()
	sub := filepath.Join(repo, "pkg", "x")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	noEnv := func(string) string { return "" }
	env := func(k string) string {
		if k == "AIDER_CHAT_HISTORY_FILE" {
			return "env.md"
		}
		return ""
	}
	cases := []struct {
		name       string
		args       []string
		getenv     func(string) string
		path, root string
	}{
		{"default at git root", nil, noEnv, filepath.Join(repo, HistoryFile), repo},
		{"flag", []string{"--model", "x", "--chat-history-file", "h.md"}, noEnv, filepath.Join(sub, "h.md"), repo},
		{"flag=", []string{"--chat-history-file=" + filepath.Join(repo, "abs.md")}, env, filepath.Join(repo, "abs.md"), repo},
		{"env", nil, env, filepath.Join(sub, "env.md"), repo},
		{"no git", []string{"--no-git"}, noEnv, filepath.Join(sub, HistoryFile), sub},
	}
	for _, c := range cases {
		path, root := HistoryPath(c.args, c.getenv, sub)
		if path != c.path || root != c.root {
			t.Errorf("%s: got %s, %s; want %s, %s", c.name, path, root, c.path, c.root)
		}
	}
}
