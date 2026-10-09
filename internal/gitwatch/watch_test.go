// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package gitwatch

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func commit(t *testing.T, dir, file, content, msg string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(filepath.Join(dir, file)), 0o755)
	os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644)
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", msg)
}

func TestWatcherSeesNewCommitsOnly(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := filepath.Join(t.TempDir(), "r")
	os.MkdirAll(repo, 0o755)
	git(t, repo, "init", "-q", "-b", "main")
	commit(t, repo, "a.txt", "1\n", "old work")

	w := NewWatcher()
	w.Watch(filepath.Join(repo)) // existing history isn't reported
	if got := w.Check(); len(got) != 0 {
		t.Fatalf("existing commits reported: %v", got)
	}
	commit(t, repo, "src/b.go", "x\ny\n", "feat: add b")
	commit(t, repo, "a.txt", "1\n2\n", "fix: extend a")
	got := w.Check()
	if len(got) != 2 || got[0].Subject != "feat: add b" || got[1].Subject != "fix: extend a" {
		t.Fatalf("commits: %+v", got)
	}
	if got[0].LinesAdded != 2 || len(got[0].Files) != 1 || got[0].Files[0] != filepath.Join(repo, "src", "b.go") {
		t.Fatalf("stats: %+v", got[0])
	}
	if again := w.Check(); len(again) != 0 {
		t.Fatalf("reported twice: %v", again)
	}

	// Switching to an old branch doesn't report old commits as new.
	git(t, repo, "branch", "old", "HEAD~2")
	git(t, repo, "checkout", "-q", "old")
	if got := w.Check(); len(got) != 0 {
		t.Fatalf("branch switch reported: %v", got)
	}
}

func TestWatcherRepoWithoutCommitsYet(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := filepath.Join(t.TempDir(), "fresh")
	os.MkdirAll(repo, 0o755)
	git(t, repo, "init", "-q", "-b", "main")
	w := NewWatcher()
	w.Watch(repo)
	if w.Watching() != 1 {
		t.Fatal("fresh repo not tracked")
	}
	commit(t, repo, "first.txt", "hi\n", "initial commit")
	if got := w.Check(); len(got) != 1 || got[0].Subject != "initial commit" {
		t.Fatalf("first commit: %+v", got)
	}
}

func TestWatchIgnoresNonRepos(t *testing.T) {
	w := NewWatcher()
	w.Watch(t.TempDir())
	if w.Watching() != 0 {
		t.Fatal("non-repo tracked")
	}
}
