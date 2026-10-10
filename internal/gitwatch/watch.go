// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

package gitwatch

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Commit is a new commit seen in a worktree.
type Commit struct {
	SHA          string
	Subject      string
	Author       string
	When         time.Time
	Worktree     string   // absolute worktree root
	Files        []string // absolute paths
	LinesAdded   int
	LinesRemoved int
}

// Watcher notices new commits in worktrees it has been told about. It
// only stats each worktree's reflog per check and runs git when the
// reflog changed, so idle repos cost nothing.
type Watcher struct {
	Git       Exec
	worktrees map[string]*worktree // by root
}

type worktree struct {
	reflog  string
	modTime time.Time
	size    int64
	head    string
	since   time.Time // commits older than this aren't "new" (branch switches, rebases)
}

// NewWatcher returns an empty watcher.
func NewWatcher() *Watcher { return &Watcher{worktrees: map[string]*worktree{}} }

// Watch starts tracking the worktree that contains dir (no-op if dir
// isn't in a repo or is already tracked).
func (w *Watcher) Watch(dir string) {
	top := w.Git.run(dir, "rev-parse", "--show-toplevel")
	if top == "" {
		return
	}
	root := agentRoot(dir, filepath.FromSlash(top))
	if _, ok := w.worktrees[root]; ok {
		return
	}
	gitDir := w.Git.run(root, "rev-parse", "--absolute-git-dir")
	if gitDir == "" {
		return
	}
	wt := &worktree{reflog: filepath.Join(gitDir, "logs", "HEAD"), head: w.Git.run(root, "rev-parse", "HEAD"), since: time.Now().Add(-time.Minute)}
	if fi, err := os.Stat(wt.reflog); err == nil {
		wt.modTime, wt.size = fi.ModTime(), fi.Size()
	}
	w.worktrees[root] = wt
}

// Watching reports how many worktrees are tracked.
func (w *Watcher) Watching() int { return len(w.worktrees) }

// Check returns commits made since the last check, oldest first.
func (w *Watcher) Check() []Commit {
	var out []Commit
	for root, wt := range w.worktrees {
		fi, err := os.Stat(wt.reflog)
		if err != nil {
			if wt.head == "" { // repo had no commits yet when first seen
				if h := w.Git.run(root, "rev-parse", "HEAD"); h != "" {
					wt.head = h
					out = append(out, w.commits(root, "", h, wt.since)...)
				}
			}
			continue
		}
		if fi.ModTime().Equal(wt.modTime) && fi.Size() == wt.size {
			continue
		}
		wt.modTime, wt.size = fi.ModTime(), fi.Size()
		head := w.Git.run(root, "rev-parse", "HEAD")
		if head == "" || head == wt.head {
			continue
		}
		out = append(out, w.commits(root, wt.head, head, wt.since)...)
		wt.head = head
	}
	return out
}

// commits lists commits reachable from `to` but not from `from`, made at
// or after `since`, oldest first. The time filter keeps branch switches
// and rebases from reporting old work as new.
func (w *Watcher) commits(root, from, to string, since time.Time) []Commit {
	rng := to
	if from != "" {
		rng = from + ".." + to
	}
	log := w.Git.run(root, "log", "--no-merges", "--max-count=50", "--format=%H%x1f%ct%x1f%an%x1f%s", rng)
	var out []Commit
	for _, line := range strings.Split(log, "\n") {
		f := strings.Split(line, "\x1f")
		if len(f) != 4 {
			continue
		}
		sec, _ := strconv.ParseInt(f[1], 10, 64)
		c := Commit{SHA: f[0], When: time.Unix(sec, 0), Author: f[2], Subject: f[3], Worktree: root}
		if c.When.Before(since) {
			continue
		}
		for _, l := range strings.Split(w.Git.run(root, "show", "--numstat", "--format=", c.SHA), "\n") {
			p := strings.SplitN(l, "\t", 3)
			if len(p) != 3 {
				continue
			}
			a, _ := strconv.Atoi(p[0]) // "-" for binary files counts as 0
			r, _ := strconv.Atoi(p[1])
			c.LinesAdded += a
			c.LinesRemoved += r
			c.Files = append(c.Files, filepath.Join(root, filepath.FromSlash(p[2])))
		}
		out = append(out, c)
	}
	// git log is newest first.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// agentRoot expresses git's worktree root in the same form as the path
// the agent used (dir). Git reports canonical paths (symlinks resolved,
// e.g. /private/var on macOS; forward slashes and long names on
// Windows), while agents report the paths they work with. Committed
// files are joined to this root, so they match the session's own paths.
func agentRoot(dir, top string) string {
	dir = filepath.Clean(dir)
	canonDir, err1 := filepath.EvalSymlinks(dir)
	canonTop, err2 := filepath.EvalSymlinks(top)
	if err1 != nil || err2 != nil {
		return filepath.Clean(top)
	}
	rel, err := filepath.Rel(canonTop, canonDir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.Clean(top)
	}
	if rel == "." {
		return dir
	}
	if r := strings.TrimSuffix(dir, string(filepath.Separator)+rel); r != dir {
		return r
	}
	return filepath.Clean(top)
}
