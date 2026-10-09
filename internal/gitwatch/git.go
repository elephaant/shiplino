// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package gitwatch

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Exec answers questions about repositories with the git binary. Every
// call is read-only and uses --no-optional-locks, so it never competes
// with the user's own git commands.
type Exec struct {
	Timeout time.Duration // per command; default 2s
}

func (g Exec) run(dir string, args ...string) string {
	t := g.Timeout
	if t == 0 {
		t = 2 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), t)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-optional-locks", "-C", dir}, args...)...)
	cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// CommonDir implements projects.Git.
func (g Exec) CommonDir(dir string) string {
	c := g.run(dir, "rev-parse", "--git-common-dir")
	if c == "" {
		return ""
	}
	if !filepath.IsAbs(c) {
		c = filepath.Join(dir, c)
	}
	return filepath.Clean(c)
}

// Remote implements projects.Git: origin, else the first remote.
func (g Exec) Remote(dir string) string {
	if u := g.run(dir, "remote", "get-url", "origin"); u != "" {
		return u
	}
	remotes := strings.Fields(g.run(dir, "remote"))
	if len(remotes) == 0 {
		return ""
	}
	return g.run(dir, "remote", "get-url", remotes[0])
}

// Branch returns the current branch ("" when detached or not a repo).
func (g Exec) Branch(dir string) string {
	b := g.run(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if b == "HEAD" {
		return ""
	}
	return b
}

// Head returns the short commit id of HEAD.
func (g Exec) Head(dir string) string { return g.run(dir, "rev-parse", "--short", "HEAD") }
