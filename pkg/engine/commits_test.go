// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

package engine

import (
	"sort"
	"testing"
	"time"

	"github.com/elephaant/shiplino/pkg/model"
)

func TestIsGitCommit(t *testing.T) {
	yes := []string{`git commit -m "x"`, "git -C /repo commit -am x", "cd app && git add . && git commit -m fix", "/usr/bin/git commit --amend", `git -c user.name=a commit -m x`, "make test; git commit -am wip"}
	no := []string{"git status", "git log --grep commit", "echo git commit", "npm run commit-lint", "gitcommit"}
	for _, c := range yes {
		if !isGitCommit(c) {
			t.Errorf("not detected: %s", c)
		}
	}
	for _, c := range no {
		if isGitCommit(c) {
			t.Errorf("false positive: %s", c)
		}
	}
}

// actor makes a root session in /work/api that edited files.
func actor(e *Engine, id string, at time.Time, files ...string) *Session {
	s := &Session{ID: id, RootID: id, CWD: "/work/api", StartedAt: at.Add(-time.Hour), LastEventAt: at, Files: files}
	e.sessions[id] = s
	return s
}

func ids(a []Attribution) []string {
	var out []string
	for _, x := range a {
		out = append(out, x.SessionID+":"+x.Confidence)
	}
	sort.Strings(out)
	return out
}

func TestAttributeCommit(t *testing.T) {
	when := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	c := CommitInfo{When: when, Worktree: "/work/api", Files: []string{"/work/api/src/auth.ts"}}

	e := New(nil, nil)
	actor(e, "a", when.Add(-2*time.Minute), "/work/api/src/auth.ts")
	actor(e, "b", when.Add(-2*time.Minute), "/work/api/README.md")
	if got := ids(e.AttributeCommit(c)); len(got) != 1 || got[0] != "a:likely" {
		t.Fatalf("one editor: %v", got)
	}

	// Two sessions edited the file: shared.
	actor(e, "c", when.Add(-time.Minute), "/work/api/src/auth.ts")
	if got := ids(e.AttributeCommit(c)); len(got) != 2 || got[0] != "a:shared" || got[1] != "c:shared" {
		t.Fatalf("two editors: %v", got)
	}

	// The agent ran `git commit` itself: exact, and it wins.
	e.sessions["b"].LastGitCommitAt = when.Add(10 * time.Second)
	if got := ids(e.AttributeCommit(c)); len(got) != 1 || got[0] != "b:exact" {
		t.Fatalf("ran git commit: %v", got)
	}

	// Stale sessions and other worktrees don't count.
	e2 := New(nil, nil)
	actor(e2, "old", when.Add(-time.Hour), "/work/api/src/auth.ts")
	other := actor(e2, "elsewhere", when, "/work/api/src/auth.ts")
	other.CWD = "/work/web"
	if got := e2.AttributeCommit(c); len(got) != 0 {
		t.Fatalf("stale/other: %v", ids(got))
	}

	// Subagent edits count for the root session.
	e3 := New(nil, nil)
	actor(e3, "root", when)
	e3.sessions["root/sub:x"] = &Session{ID: "root/sub:x", RootID: "root", ParentID: "root", StartedAt: when.Add(-5 * time.Minute), LastEventAt: when, Files: []string{"/work/api/src/auth.ts"}}
	if got := ids(e3.AttributeCommit(c)); len(got) != 1 || got[0] != "root:likely" {
		t.Fatalf("subagent edit: %v", got)
	}
}

func TestCommitMovesReviewToDone(t *testing.T) {
	e := New(nil, nil)
	e.Apply(ev(0, model.KindTurnStart, nil))
	e.Apply(ev(1, model.KindFileEdit, map[string]any{"path": "/work/demo/a.go"}))
	e.Apply(ev(2, model.KindTurnEnd, map[string]any{"status": "ok"}))
	if s := e.Get(sid); s.Status != StatusReview {
		t.Fatalf("status = %s", s.Status)
	}
	e.Apply(ev(3, model.KindShellExec, map[string]any{"command": "git commit -m done", "exit_code": 0}))
	e.Apply(ev(4, model.KindGitCommit, map[string]any{"sha": "abc1234", "message": "done", "attribution": "exact"}))
	s := e.Get(sid)
	if s.Status != StatusDone || len(s.Links) != 1 || s.Links[0].Kind != "commit" || s.LastGitCommitAt.IsZero() {
		t.Fatalf("after commit: status=%s links=%+v", s.Status, s.Links)
	}
}
