// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package daemon

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestSessionsLandInTheirProject(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	withHome(t)
	e := newEnv(t)
	repo := filepath.Join(t.TempDir(), "api")
	os.MkdirAll(repo, 0o755)
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "remote", "add", "origin", "https://github.com/acme/api.git")
	os.WriteFile(filepath.Join(repo, "x"), []byte("x"), 0o644)
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-q", "-m", "init")
	wt := filepath.Join(filepath.Dir(repo), "api-payments")
	gitIn(t, repo, "worktree", "add", "-q", "-b", "payments", wt)

	start := func(session, cwd string) string {
		b, _ := json.Marshal(map[string]string{"session_id": session, "hook_event_name": "UserPromptSubmit", "prompt": "work", "cwd": cwd})
		return string(b)
	}
	e.hook(start("main-1", repo))
	e.hook(start("wt-1", wt))
	e.poll()

	a, b := e.session("claude-code:main-1"), e.session("claude-code:wt-1")
	if a.ProjectID != "github.com/acme/api" || b.ProjectID != a.ProjectID {
		t.Fatalf("projects: %q %q", a.ProjectID, b.ProjectID)
	}
	if a.Branch != "main" || b.Branch != "payments" {
		t.Fatalf("branches: %q %q", a.Branch, b.Branch)
	}
	list, err := e.st.Projects(ctx)
	if err != nil || len(list) != 1 || list[0].Name != "api" || list[0].Counts["running"] != 2 {
		t.Fatalf("projects: %+v %v", list, err)
	}
}
