package daemon

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/elephaant/shiplino/internal/gitwatch"
	"github.com/elephaant/shiplino/pkg/model"
	"github.com/elephaant/shiplino/pkg/redact"
)

func TestMatchLines(t *testing.T) {
	upper := func(s string) string { return strings.ReplaceAll(s, "sk_live_123", "«redacted:key»") }
	cases := []struct {
		name              string
		committed, edited []string
		want              int
	}{
		{"all", []string{"a", "b"}, []string{"a", "b"}, 2},
		{"human line", []string{"a", "x", "b"}, []string{"a", "b"}, 2},
		{"each edited line once", []string{"}", "}", "}"}, []string{"}"}, 1},
		{"re-indented", []string{"\tif x {"}, []string{"    if x {"}, 1},
		{"human changed it", []string{"return 2"}, []string{"return 1"}, 0},
		{"redacted in the stored diff", []string{`key := "sk_live_123"`}, []string{`key := "«redacted:key»"`}, 1},
		{"nothing edited", []string{"a"}, nil, 0},
	}
	for _, c := range cases {
		if got := matchLines(c.committed, c.edited, upper); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

func TestSplitAuthorship(t *testing.T) {
	root := filepath.Join(t.TempDir(), "api")
	f := func(n string) string { return filepath.Join(root, n) }
	c := gitwatch.Commit{Worktree: root, Files: []string{f("a.go"), f("b.go"), f("c.go"), f("img.png")},
		Added: map[string]int{f("a.go"): 3, f("b.go"): 2, f("c.go"): 4, f("img.png"): -1, f("big.go"): 9000}}
	cases := []struct {
		name                  string
		ev                    map[string]*fileEvidence
		committed             map[string][]string
		agent, human, unknown int
		evidence              string
		agentFiles            int
	}{
		{"no agent edits: all human", nil, nil, 0, 9, 0, authObserved, 0},
		{"full diffs", map[string]*fileEvidence{f("a.go"): {touched: true, full: true, added: []string{"x", "y"}}},
			map[string][]string{f("a.go"): {"x", "y", "z"}}, 2, 7, 0, authObserved, 1},
		{"below full: touched files unknown", map[string]*fileEvidence{f("a.go"): {touched: true}, f("img.png"): {touched: true}},
			nil, 0, 6, 3, authUnknown, 2},
		{"some edits without diffs", map[string]*fileEvidence{f("a.go"): {touched: true, added: []string{"x"}}},
			map[string][]string{f("a.go"): {"x", "y", "z"}}, 1, 6, 2, authPartial, 1},
		{"diff not read (too big)", map[string]*fileEvidence{f("big.go"): {touched: true, full: true, added: []string{"x"}}},
			nil, 0, 9, 9000, authUnknown, 1},
	}
	for _, tc := range cases {
		c := c
		if tc.name == "diff not read (too big)" {
			c.Files = []string{f("b.go"), f("c.go"), f("a.go"), f("big.go")}
		}
		a := splitAuthorship(c, tc.ev, tc.committed, nil)
		if a.Agent != tc.agent || a.Human != tc.human || a.Unknown != tc.unknown || a.Evidence != tc.evidence || len(a.AgentFiles) != tc.agentFiles {
			t.Errorf("%s: got %+v", tc.name, a)
		}
		if len(a.Files) != len(c.Files) || a.Files[0].Path != strings.TrimPrefix(filepath.ToSlash(c.Files[0]), filepath.ToSlash(root)+"/") {
			t.Errorf("%s: files %+v", tc.name, a.Files)
		}
	}
}

// commitAgentWork runs a synthetic session that writes src/auth.ts, then
// the user adds a line to it and another file, and commits.
func commitAgentWork(t *testing.T, level redact.Level) (*env, map[string]any) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	withHome(t)
	e := newEnv(t)
	e.d.SetPrivacy(level, redact.Default)
	repo := filepath.Join(t.TempDir(), "api")
	os.MkdirAll(filepath.Join(repo, "src"), 0o755)
	gitIn(t, repo, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(repo, "README.md"), []byte("x\n"), 0o644)
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-q", "-m", "init")

	file := filepath.Join(repo, "src", "auth.ts")
	for _, h := range []map[string]any{
		{"session_id": "c1", "hook_event_name": "UserPromptSubmit", "prompt": "fix auth", "cwd": repo},
		{"session_id": "c1", "hook_event_name": "PostToolUse", "tool_name": "Write", "tool_input": map[string]any{"file_path": file, "content": "export const a = 1;\nexport const b = 2;\n"},
			"tool_response": map[string]any{"type": "create"}, "tool_use_id": "t1", "cwd": repo},
		{"session_id": "c1", "hook_event_name": "Stop", "cwd": repo},
	} {
		b, _ := json.Marshal(h)
		e.hook(string(b))
	}
	e.poll()

	os.WriteFile(file, []byte("export const a = 1;\nexport const b = 2;\nexport const c = 3;\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "README.md"), []byte("x\ny\n"), 0o644)
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-q", "-m", "fix: auth")
	e.poll()

	evs, err := e.st.Events(ctx, "claude-code:c1", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		if ev.Kind == model.KindGitCommit {
			return e, ev.Data
		}
	}
	t.Fatal("no commit event")
	return nil, nil
}

func TestCommitAuthorshipAtFull(t *testing.T) {
	e, d := commitAgentWork(t, redact.Full)
	if d["agent_lines_added"] != 2.0 || d["human_lines_added"] != 2.0 || d["unknown_lines_added"] != 0.0 || d["authorship"] != "observed" {
		t.Fatalf("authorship: %v", d)
	}
	if files, _ := d["agent_files"].([]any); len(files) != 1 || files[0] != "src/auth.ts" {
		t.Fatalf("agent files: %v", d["agent_files"])
	}
	l := e.session("claude-code:c1").Links[0]
	if l.Lines != 4 || l.AgentLines != 2 || l.HumanLines != 2 || l.AgentFiles != 1 || l.Authorship != "observed" {
		t.Fatalf("link: %+v", l)
	}
	rows, err := e.st.CommitLines(ctx, time.Unix(0, 0), time.Now().Add(time.Hour), "")
	if err != nil || len(rows) != 1 || rows[0].AgentLines != 2 || rows[0].HumanLines != 2 || rows[0].Agent != "claude-code" {
		t.Fatalf("commit lines: %+v %v", rows, err)
	}
}

func TestCommitAuthorshipBelowFull(t *testing.T) {
	_, d := commitAgentWork(t, redact.Standard)
	if d["agent_lines_added"] != 0.0 || d["human_lines_added"] != 1.0 || d["unknown_lines_added"] != 3.0 || d["authorship"] != "unknown" {
		t.Fatalf("authorship: %v", d)
	}
	if files, _ := d["agent_files"].([]any); len(files) != 1 || files[0] != "src/auth.ts" {
		t.Fatalf("agent files: %v", d["agent_files"])
	}
}

func TestAddedLinesBounded(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	gitIn(t, repo, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(repo, "a b.txt"), []byte("one\ntwo\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "bin.dat"), []byte{0, 1, 2, 0, 3}, 0o644)
	os.WriteFile(filepath.Join(repo, "skip.txt"), []byte("no\n"), 0o644)
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-q", "-m", "init")
	out, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	sha := strings.TrimSpace(string(out))
	got, err := gitwatch.Exec{}.AddedLines(repo, sha, []string{filepath.Join(repo, "a b.txt"), filepath.Join(repo, "bin.dat")})
	if err != nil {
		t.Fatal(err)
	}
	if l := got[filepath.Join(repo, "a b.txt")]; len(l) != 2 || l[0] != "one" || len(got) != 1 {
		t.Fatalf("added: %q", got)
	}
}
