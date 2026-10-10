package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/elephaant/shiplino/internal/store"
	"github.com/elephaant/shiplino/pkg/model"
)

func TestGitNotes(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	user := t.TempDir()
	t.Setenv("HOME", user)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, kv := range [][2]string{{"GIT_AUTHOR_NAME", "t"}, {"GIT_AUTHOR_EMAIL", "t@e"}, {"GIT_COMMITTER_NAME", "t"}, {"GIT_COMMITTER_EMAIL", "t@e"}} {
		t.Setenv(kv[0], kv[1])
	}
	repo := filepath.Join(user, "api")
	os.MkdirAll(repo, 0o755)
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"commit", "-q", "--allow-empty", "-m", "work"}} {
		if _, err := runGit(repo, nil, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	sha, _ := runGit(repo, nil, "rev-parse", "HEAD")

	var out bytes.Buffer
	e := &env{out: &out, errOut: &out, home: filepath.Join(user, ".shiplino"), userHome: user, version: "test"}
	st, err := store.Open(filepath.Join(e.home, "data", "shiplino.db"))
	if err != nil {
		t.Fatal(err)
	}
	tx, _ := st.Begin(ctx)
	for _, sid := range []string{"claude-code:s1", "codex:s2"} { // a shared commit gets one note
		_, err := tx.InsertEvent(ctx, model.Event{ID: model.NewULID(time.Now()), Kind: model.KindGitCommit, TS: time.Now(), SessionID: sid,
			Agent: model.Agent{Name: strings.Split(sid, ":")[0]}, Collector: model.CollectorGit, DedupKey: sid + ":git:" + sha,
			Project: &model.Project{CWD: repo, RepoRoot: repo},
			Data: map[string]any{"sha": sha, "lines_added": 10, "agent_lines_added": 7, "human_lines_added": 3, "unknown_lines_added": 0,
				"authorship": "observed", "agent_files": []any{"a.go"}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	st.Close()

	// Off by default: nothing is written.
	if code := gitCmd(ctx, e, []string{"notes", repo}); code != 1 || !strings.Contains(out.String(), "notes = true") {
		t.Fatalf("default: %d %s", code, out.String())
	}
	if _, err := runGit(repo, nil, "notes", "--ref=shiplino", "show", sha); err == nil {
		t.Fatal("note written while off")
	}
	out.Reset()
	if code := gitCmd(ctx, e, []string{"notes", "--dry-run", repo}); code != 0 || !strings.Contains(out.String(), "Agent lines: 7") {
		t.Fatalf("dry run: %d %s", code, out.String())
	}

	os.WriteFile(filepath.Join(e.home, "config.toml"), []byte("[git]\nnotes = true\n"), 0o600)
	out.Reset()
	if code := gitCmd(ctx, e, []string{"notes", repo}); code != 0 || !strings.Contains(out.String(), "Wrote 1 notes") {
		t.Fatalf("write: %d %s", code, out.String())
	}
	note, err := runGit(repo, nil, "notes", "--ref=shiplino", "show", sha)
	if err != nil || !strings.Contains(note, "Agent lines: 7") || !strings.Contains(note, "Agents: claude-code, codex") {
		t.Fatalf("note: %q %v", note, err)
	}
	// Running again replaces the note instead of failing.
	if code := gitCmd(ctx, e, []string{"notes", repo}); code != 0 {
		t.Fatalf("rewrite: %s", out.String())
	}
}
