// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package gitwatch

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/elephaant/shiplino/pkg/projects"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestExecWithRealRepoAndWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	home := t.TempDir()
	repo := filepath.Join(home, "api")
	os.MkdirAll(filepath.Join(repo, "sub"), 0o755)
	git(t, repo, "init", "-q", "-b", "main")
	git(t, repo, "remote", "add", "origin", "git@github.com:acme/api.git")
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a"), 0o644)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "init")
	wt := filepath.Join(home, "api-feature")
	git(t, repo, "worktree", "add", "-q", "-b", "feature", wt)

	g := Exec{}
	for _, dir := range []string{repo, filepath.Join(repo, "sub"), wt} {
		p := projects.Detect(dir, g, home)
		if p.ID != "github.com/acme/api" || p.Name != "api" {
			t.Errorf("Detect(%s) = %+v", dir, p)
		}
	}
	if b := g.Branch(wt); b != "feature" {
		t.Errorf("worktree branch = %q", b)
	}
	if b := g.Branch(repo); b != "main" {
		t.Errorf("branch = %q", b)
	}
	if len(g.Head(repo)) < 7 {
		t.Error("no head")
	}
	plain := filepath.Join(home, "plain")
	os.MkdirAll(plain, 0o755)
	if g.CommonDir(plain) != "" || g.Branch(plain) != "" {
		t.Error("non-repo detected as repo")
	}
}
