// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package daemon

import (
	"os"
	"time"

	"github.com/elephaant/shiplino/internal/gitwatch"
	"github.com/elephaant/shiplino/pkg/model"
	"github.com/elephaant/shiplino/pkg/projects"
)

const (
	projectTTL = 10 * time.Minute // a folder's project rarely changes
	branchTTL  = 5 * time.Second  // branches change during work
)

// resolver maps working directories to projects and branches, caching
// git lookups so a busy session costs at most one git call per TTL.
type resolver struct {
	git      gitwatch.Exec
	userHome string
	byCWD    map[string]cachedProject
	byID     map[string]projects.Project
	branches map[string]cachedBranch
	now      func() time.Time
}

type cachedProject struct {
	p     projects.Project
	until time.Time
}

type cachedBranch struct {
	branch string
	until  time.Time
}

func newResolver() *resolver {
	home, _ := os.UserHomeDir()
	return &resolver{userHome: home, byCWD: map[string]cachedProject{}, byID: map[string]projects.Project{}, branches: map[string]cachedBranch{}, now: time.Now}
}

func (r *resolver) project(cwd string) projects.Project {
	if c, ok := r.byCWD[cwd]; ok && r.now().Before(c.until) {
		return c.p
	}
	p := projects.Detect(cwd, r.git, r.userHome)
	r.byCWD[cwd] = cachedProject{p: p, until: r.now().Add(projectTTL)}
	r.byID[p.ID] = p
	return p
}

func (r *resolver) branch(dir string) string {
	if c, ok := r.branches[dir]; ok && r.now().Before(c.until) {
		return c.branch
	}
	b := r.git.Branch(dir)
	r.branches[dir] = cachedBranch{branch: b, until: r.now().Add(branchTTL)}
	return b
}

// annotate fills in the event's project identity (and the branch at the
// start of a session or turn) from its working directory.
func (r *resolver) annotate(e *model.Event) {
	if e.Project == nil || e.Project.CWD == "" {
		return
	}
	p := r.project(e.Project.CWD)
	e.Project.ID = p.ID
	if e.Project.RepoRoot == "" {
		e.Project.RepoRoot = p.RepoRoot
	}
	if e.Project.Remote == "" {
		e.Project.Remote = p.Remote
	}
	if p.Kind == "remote" || p.Kind == "git" {
		switch e.Kind {
		case model.KindSessionStart, model.KindTurnStart:
			if e.Project.Branch == "" {
				e.Project.Branch = r.branch(e.Project.CWD)
			}
		}
	}
}
