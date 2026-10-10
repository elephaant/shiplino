// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

package engine

import (
	"path/filepath"
	"strings"
	"time"
)

// CommitInfo is what attribution needs to know about a commit.
type CommitInfo struct {
	When     time.Time
	Worktree string   // absolute worktree root
	Files    []string // absolute paths
}

// Attribution links a commit to a root session.
type Attribution struct {
	SessionID  string
	Confidence string // "exact" | "likely" | "shared"
}

const (
	commitWindow = 2 * time.Minute  // agent ran `git commit` this close to the commit
	activeWindow = 10 * time.Minute // session edited files this recently
)

// Sessions returns every session (roots and subagents).
func (e *Engine) Sessions() []*Session {
	out := make([]*Session, 0, len(e.sessions))
	for _, s := range e.sessions {
		out = append(out, s)
	}
	return out
}

// AttributeCommit decides which root sessions produced a commit:
//   - exact: the agent itself ran `git commit` around the commit time
//   - likely: the session (or its subagents) recently edited committed files
//   - shared: several sessions edited the committed files
func (e *Engine) AttributeCommit(c CommitInfo) []Attribution {
	committed := map[string]bool{}
	for _, f := range c.Files {
		committed[filepath.Clean(f)] = true
	}
	type cand struct {
		root   *Session
		edited bool
		ranGit bool
	}
	cands := map[string]*cand{}
	for _, s := range e.sessions {
		root := e.sessions[s.RootID]
		if root == nil || !within(root.CWD, c.Worktree) {
			continue
		}
		if s.LastEventAt.Before(c.When.Add(-activeWindow)) || s.StartedAt.After(c.When.Add(commitWindow)) {
			continue // not active around the commit
		}
		cd := cands[root.ID]
		if cd == nil {
			cd = &cand{root: root}
			cands[root.ID] = cd
		}
		for _, f := range s.Files {
			if committed[filepath.Clean(f)] {
				cd.edited = true
			}
		}
		if !s.LastGitCommitAt.IsZero() && absDur(s.LastGitCommitAt.Sub(c.When)) <= commitWindow {
			cd.ranGit = true
		}
	}
	var exact, edited []string
	for id, cd := range cands {
		if cd.ranGit {
			exact = append(exact, id)
		} else if cd.edited {
			edited = append(edited, id)
		}
	}
	var out []Attribution
	switch {
	case len(exact) > 0:
		for _, id := range exact {
			out = append(out, Attribution{SessionID: id, Confidence: "exact"})
		}
	case len(edited) == 1:
		out = append(out, Attribution{SessionID: edited[0], Confidence: "likely"})
	default:
		for _, id := range edited {
			out = append(out, Attribution{SessionID: id, Confidence: "shared"})
		}
	}
	return out
}

func within(dir, root string) bool {
	if dir == "" || root == "" {
		return false
	}
	dir, root = filepath.Clean(dir), filepath.Clean(root)
	return dir == root || strings.HasPrefix(dir, root+string(filepath.Separator))
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
