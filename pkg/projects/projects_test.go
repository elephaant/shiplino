// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

package projects

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeRemote(t *testing.T) {
	cases := map[string]string{
		"git@github.com:acme/api.git":                 "github.com/acme/api",
		"https://github.com/acme/api.git":             "github.com/acme/api",
		"https://user:tok@GitHub.com/acme/api":        "github.com/acme/api",
		"ssh://git@gitlab.example.com:2222/g/s/r.git": "gitlab.example.com/g/s/r",
		"git://host.xz/path/to/repo.git/":             "host.xz/path/to/repo",
		"github.com:acme/api":                         "github.com/acme/api",
		"/srv/git/repo.git":                           "",
		"file:///srv/git/repo.git":                    "",
		`C:\repos\api`:                                "",
		"":                                            "",
	}
	for in, want := range cases {
		if got := NormalizeRemote(in); got != want {
			t.Errorf("NormalizeRemote(%q) = %q, want %q", in, got, want)
		}
	}
}

type fakeGit struct{ common, remote map[string]string }

func (f fakeGit) CommonDir(dir string) string { return f.common[dir] }
func (f fakeGit) Remote(dir string) string    { return f.remote[dir] }

func TestDetect(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "code", "api")
	wt := filepath.Join(home, "code", "api-wt-payments")
	scratch := filepath.Join(home, "code", "scratch")
	notes := filepath.Join(home, "notes", "sub")
	node := filepath.Join(home, "code", "site", "src", "pages")
	os.MkdirAll(notes, 0o755)
	os.MkdirAll(node, 0o755)
	os.WriteFile(filepath.Join(home, "code", "site", "package.json"), []byte("{}"), 0o644)

	g := fakeGit{
		common: map[string]string{
			repo:                       filepath.Join(repo, ".git"),
			filepath.Join(repo, "pkg"): filepath.Join(repo, ".git"),
			wt:                         filepath.Join(repo, ".git"), // worktree shares the common dir
			scratch:                    filepath.Join(scratch, ".git"),
		},
		remote: map[string]string{
			repo: "git@github.com:acme/api.git", filepath.Join(repo, "pkg"): "git@github.com:acme/api.git",
			wt: "git@github.com:acme/api.git",
		},
	}
	check := func(cwd, wantID, wantName, wantKind string) {
		t.Helper()
		p := Detect(cwd, g, home)
		if p.ID != wantID || p.Name != wantName || p.Kind != wantKind {
			t.Errorf("Detect(%s) = %+v, want %s %s %s", cwd, p, wantID, wantName, wantKind)
		}
	}
	check(repo, "github.com/acme/api", "api", "remote")
	check(filepath.Join(repo, "pkg"), "github.com/acme/api", "api", "remote")
	check(wt, "github.com/acme/api", "api", "remote") // worktree → same project
	check(scratch, "local:"+filepath.ToSlash(scratch), "scratch", "git")
	check(node, "dir:"+filepath.ToSlash(filepath.Join(home, "code", "site")), "site", "dir")
	check(notes, "dir:"+filepath.ToSlash(notes), "sub", "dir")
	check(home, "unsorted", "Unsorted", "unsorted")
	check(os.TempDir(), "unsorted", "Unsorted", "unsorted")
	check("", "unsorted", "Unsorted", "unsorted")

	// The worktree's repo root is the main checkout.
	if p := Detect(wt, g, home); p.RepoRoot != repo {
		t.Errorf("worktree repo root = %s, want %s", p.RepoRoot, repo)
	}
}
