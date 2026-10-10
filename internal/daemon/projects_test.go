// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package daemon

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/elephaant/shiplino/internal/shim"
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

func TestCommitLinksSessionAndMovesCardToDone(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	withHome(t)
	e := newEnv(t)
	repo := filepath.Join(t.TempDir(), "api")
	os.MkdirAll(filepath.Join(repo, "src"), 0o755)
	gitIn(t, repo, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(repo, "README.md"), []byte("x\n"), 0o644)
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-q", "-m", "init")

	file := filepath.Join(repo, "src", "auth.ts")
	hooks := []map[string]any{
		{"session_id": "c1", "hook_event_name": "UserPromptSubmit", "prompt": "fix auth", "cwd": repo},
		{"session_id": "c1", "hook_event_name": "PostToolUse", "tool_name": "Write", "tool_input": map[string]any{"file_path": file, "content": "a\nb\n"},
			"tool_response": map[string]any{"type": "create"}, "tool_use_id": "t1", "cwd": repo},
		{"session_id": "c1", "hook_event_name": "Stop", "cwd": repo},
	}
	for _, h := range hooks {
		b, _ := json.Marshal(h)
		e.hook(string(b))
	}
	e.poll()
	if s := e.session("claude-code:c1"); s.Status != "review" {
		t.Fatalf("before commit: %s", s.Status)
	}

	// The user commits the agent's work.
	os.WriteFile(file, []byte("a\nb\n"), 0o644)
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-q", "-m", "fix: auth redirect")
	e.poll()

	s := e.session("claude-code:c1")
	if s.Status != "done" {
		t.Fatalf("after commit: %s", s.Status)
	}
	if len(s.Links) != 1 || s.Links[0].Kind != "commit" || s.Links[0].Message != "fix: auth redirect" || s.Links[0].Action != "likely" {
		t.Fatalf("links: %+v", s.Links)
	}
	// Polling again doesn't add the commit twice.
	e.poll()
	if s := e.session("claude-code:c1"); len(s.Links) != 1 {
		t.Fatalf("duplicate link: %+v", s.Links)
	}
}

// Windsurf's prompt hook has no folder: the project, git watch and commit
// link come from the first file event.
func TestWindsurfSessionFindsItsProject(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	withHome(t)
	e := newEnv(t)
	repo := filepath.Join(t.TempDir(), "api")
	os.MkdirAll(filepath.Join(repo, "src"), 0o755)
	gitIn(t, repo, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(repo, "README.md"), []byte("x\n"), 0o644)
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-q", "-m", "init")

	file := filepath.Join(repo, "src", "auth.ts")
	for _, h := range []map[string]any{
		{"agent_action_name": "pre_user_prompt", "trajectory_id": "w1", "execution_id": "x1", "tool_info": map[string]any{"user_prompt": "fix auth"}},
		{"agent_action_name": "post_write_code", "trajectory_id": "w1", "execution_id": "x1", "tool_info": map[string]any{"file_path": file,
			"edits": []map[string]string{{"old_string": "", "new_string": "a\nb\n"}}}},
		{"agent_action_name": "post_cascade_response", "trajectory_id": "w1", "execution_id": "x1", "tool_info": map[string]any{"response": "done"}},
	} {
		b, _ := json.Marshal(h)
		shim.Run([]string{"--agent", "windsurf"}, strings.NewReader(string(b)))
	}
	e.poll()
	s := e.session("windsurf:w1")
	if s.Status != "review" || s.ProjectID == "" || s.CWD != filepath.Join(repo, "src") || s.LinesAdded != 2 || s.Title != "fix auth" {
		t.Fatalf("session: %+v", s)
	}

	os.WriteFile(file, []byte("a\nb\n"), 0o644)
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-q", "-m", "fix: auth")
	e.poll()
	if s := e.session("windsurf:w1"); s.Status != "done" || len(s.Links) != 1 {
		t.Fatalf("after commit: %s %+v", s.Status, s.Links)
	}
}
