// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

package projects

import (
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// Project identifies where agent work belongs. The ID is stable across
// machines when the repo has a remote, so team views merge naturally.
type Project struct {
	ID       string `json:"id"`   // e.g. "github.com/acme/api", "local:/home/u/scratch", "dir:/home/u/notes", "unsorted"
	Name     string `json:"name"` // e.g. "api"
	Remote   string `json:"remote,omitempty"`
	RepoRoot string `json:"repo_root,omitempty"` // main worktree root
	Kind     string `json:"kind"`                // "remote" | "git" | "dir" | "unsorted"
}

// Unsorted holds work started in places that aren't a project (home dir, /tmp).
var Unsorted = Project{ID: "unsorted", Name: "Unsorted", Kind: "unsorted"}

// Git answers the few questions detection needs about a directory. The
// local daemon implements it with the git binary; tests use fakes.
type Git interface {
	// CommonDir returns the absolute git common dir for a path inside a
	// repo (shared by all worktrees), or "" if dir isn't in a repo.
	CommonDir(dir string) string
	// Remote returns the URL of the "origin" remote (or the first
	// remote), or "".
	Remote(dir string) string
}

// markers are files that make a non-git folder a project root.
var markers = []string{
	"go.mod", "package.json", "pyproject.toml", "setup.py", "Cargo.toml", "pom.xml", "build.gradle",
	"build.gradle.kts", "Gemfile", "composer.json", "mix.exs", "deno.json", "Package.swift", ".shiplino.toml",
}

// Detect decides which project a working directory belongs to.
func Detect(cwd string, git Git, userHome string) Project {
	cwd = filepath.Clean(cwd)
	if cwd == "" || cwd == "." {
		return Unsorted
	}
	if common := git.CommonDir(cwd); common != "" {
		root := filepath.Clean(common)
		if filepath.Base(root) == ".git" {
			root = filepath.Dir(root)
		}
		if remote := NormalizeRemote(git.Remote(cwd)); remote != "" {
			return Project{ID: remote, Name: path.Base(remote), Remote: remote, RepoRoot: root, Kind: "remote"}
		}
		return Project{ID: "local:" + filepath.ToSlash(root), Name: filepath.Base(root), RepoRoot: root, Kind: "git"}
	}
	if isUnsortedDir(cwd, userHome) {
		return Unsorted
	}
	for dir := cwd; ; dir = filepath.Dir(dir) {
		if isUnsortedDir(dir, userHome) {
			break
		}
		for _, m := range markers {
			if _, err := os.Stat(filepath.Join(dir, m)); err == nil {
				return Project{ID: "dir:" + filepath.ToSlash(dir), Name: filepath.Base(dir), RepoRoot: dir, Kind: "dir"}
			}
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	return Project{ID: "dir:" + filepath.ToSlash(cwd), Name: filepath.Base(cwd), RepoRoot: cwd, Kind: "dir"}
}

func isUnsortedDir(dir, userHome string) bool {
	dir = filepath.Clean(dir)
	if userHome != "" && dir == filepath.Clean(userHome) {
		return true
	}
	if filepath.Dir(dir) == dir { // filesystem root
		return true
	}
	tmp := filepath.Clean(os.TempDir())
	return dir == tmp || dir == "/tmp" || dir == "/var/tmp"
}

var scpLike = regexp.MustCompile(`^(?:[\w.-]+@)?([\w.-]+):(.+)$`)

// NormalizeRemote turns any git remote URL into "host/owner/repo":
// lowercase host, no scheme, user, port or ".git" suffix. Local paths and
// unparseable values return "".
func NormalizeRemote(remote string) string {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return ""
	}
	var host, p string
	switch {
	case strings.Contains(remote, "://"):
		u, err := url.Parse(remote)
		if err != nil || u.Host == "" || u.Scheme == "file" {
			return ""
		}
		host, p = u.Hostname(), u.Path
	case scpLike.MatchString(remote) && !strings.HasPrefix(remote, "/") && !isWindowsPath(remote):
		m := scpLike.FindStringSubmatch(remote)
		host, p = m[1], m[2]
	default:
		return "" // a local path
	}
	p = strings.TrimSuffix(strings.Trim(p, "/"), ".git")
	if host == "" || p == "" {
		return ""
	}
	return strings.ToLower(host) + "/" + p
}

func isWindowsPath(s string) bool {
	return len(s) >= 2 && s[1] == ':' && (s[0] >= 'a' && s[0] <= 'z' || s[0] >= 'A' && s[0] <= 'Z')
}
