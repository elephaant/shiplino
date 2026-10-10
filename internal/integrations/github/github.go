// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

// Package github shows the state of the pull requests agents work on:
// open, draft, merged or closed, CI checks and review decision. It is off
// unless the user turns it on ([integrations.github] enabled = true), since
// it calls api.github.com.
//
// It uses GitHub's REST API (checked 2026-10-10):
//
//	GET /repos/{owner}/{repo}/pulls/{number}            state, draft, merged
//	GET /repos/{owner}/{repo}/pulls?head={owner}:{branch} a branch's PR
//	GET /repos/{owner}/{repo}/commits/{sha}/check-runs   CI for the PR head
//	GET /repos/{owner}/{repo}/pulls/{number}/reviews     review decision
//
// Conditional requests (If-None-Match) don't count against the rate limit
// when nothing changed, and the client stops early when few requests remain.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrNoToken means no GitHub token was found.
var ErrNoToken = errors.New("no GitHub token: sign in with `gh auth login`, or set GITHUB_TOKEN")

// errRateLimited stops a poll pass early.
var errRateLimited = errors.New("GitHub API rate limit nearly used up")

// Token finds a token: the GitHub CLI's (`gh auth token`), then GH_TOKEN /
// GITHUB_TOKEN. It returns where it came from, for doctor.
func Token(ctx context.Context) (token, source string, err error) {
	if bin, err := exec.LookPath("gh"); err == nil {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, bin, "auth", "token", "--hostname", "github.com").Output(); err == nil {
			if t := strings.TrimSpace(string(out)); t != "" {
				return t, "gh CLI", nil
			}
		}
	}
	for _, k := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if t := strings.TrimSpace(os.Getenv(k)); t != "" {
			return t, k, nil
		}
	}
	return "", "", ErrNoToken
}

// PR is what Shiplino shows about a pull request.
type PR struct {
	URL      string    `json:"url"`
	Repo     string    `json:"repo"` // owner/name
	Number   int       `json:"number"`
	Title    string    `json:"title,omitempty"`
	State    string    `json:"state"` // open | draft | merged | closed
	HeadSHA  string    `json:"head_sha,omitempty"`
	Branch   string    `json:"branch,omitempty"`
	Checks   string    `json:"checks,omitempty"` // success | failure | pending | "" (none)
	Review   string    `json:"review,omitempty"` // approved | changes_requested | ""
	Updated  time.Time `json:"updated_at"`
	MergedAt time.Time `json:"merged_at,omitzero"`
}

// Client is a small GitHub REST client with ETag caching.
type Client struct {
	Base  string // https://api.github.com
	Token string
	HTTP  *http.Client

	mu        sync.Mutex
	etags     map[string]string
	cache     map[string][]byte
	remaining int // from X-RateLimit-Remaining; -1 unknown
}

// New returns a client for github.com.
func New(token string) *Client {
	return &Client{Base: "https://api.github.com", Token: token, HTTP: &http.Client{Timeout: 20 * time.Second},
		etags: map[string]string{}, cache: map[string][]byte{}, remaining: -1}
}

// Remaining is the last seen rate-limit budget (-1 if unknown).
func (c *Client) Remaining() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.remaining
}

func (c *Client) get(ctx context.Context, path string, v any) error {
	c.mu.Lock()
	if c.remaining >= 0 && c.remaining < 50 {
		c.mu.Unlock()
		return errRateLimited
	}
	etag := c.etags[path]
	c.mu.Unlock()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if n, err := strconv.Atoi(resp.Header.Get("X-RateLimit-Remaining")); err == nil {
		c.mu.Lock()
		c.remaining = n
		c.mu.Unlock()
	}
	var body []byte
	switch resp.StatusCode {
	case http.StatusNotModified:
		c.mu.Lock()
		body = c.cache[path]
		c.mu.Unlock()
	case http.StatusOK:
		body, err = io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		if err != nil {
			return err
		}
		c.mu.Lock()
		if e := resp.Header.Get("ETag"); e != "" {
			c.etags[path], c.cache[path] = e, body
		}
		c.mu.Unlock()
	case http.StatusUnauthorized:
		return fmt.Errorf("GitHub rejected the token (401)")
	case http.StatusForbidden, http.StatusTooManyRequests:
		return errRateLimited
	default:
		return fmt.Errorf("GitHub %s: %s", path, resp.Status)
	}
	return json.Unmarshal(body, v)
}

type apiPR struct {
	Number   int        `json:"number"`
	HTMLURL  string     `json:"html_url"`
	Title    string     `json:"title"`
	State    string     `json:"state"` // open | closed
	Draft    bool       `json:"draft"`
	MergedAt *time.Time `json:"merged_at"`
	Updated  time.Time  `json:"updated_at"`
	Head     struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"head"`
}

func (p apiPR) toPR(repo string) PR {
	out := PR{URL: p.HTMLURL, Repo: repo, Number: p.Number, Title: p.Title, State: p.State, HeadSHA: p.Head.SHA, Branch: p.Head.Ref, Updated: p.Updated}
	switch {
	case p.MergedAt != nil:
		out.State, out.MergedAt = "merged", *p.MergedAt
	case p.State == "open" && p.Draft:
		out.State = "draft"
	}
	return out
}

// Get fetches a PR with its checks and review decision.
func (c *Client) Get(ctx context.Context, repo string, number int) (PR, error) {
	var p apiPR
	if err := c.get(ctx, fmt.Sprintf("/repos/%s/pulls/%d", repo, number), &p); err != nil {
		return PR{}, err
	}
	pr := p.toPR(repo)
	if pr.State == "open" || pr.State == "draft" {
		pr.Checks, _ = c.checks(ctx, repo, pr.HeadSHA)
		pr.Review, _ = c.review(ctx, repo, number)
	}
	return pr, nil
}

// ForBranch finds the most recent PR whose head is branch in repo.
func (c *Client) ForBranch(ctx context.Context, repo, branch string) (PR, bool, error) {
	owner, _, _ := strings.Cut(repo, "/")
	var list []apiPR
	q := url.Values{"head": {owner + ":" + branch}, "state": {"all"}, "per_page": {"5"}}
	if err := c.get(ctx, fmt.Sprintf("/repos/%s/pulls?%s", repo, q.Encode()), &list); err != nil {
		return PR{}, false, err
	}
	if len(list) == 0 {
		return PR{}, false, nil
	}
	return list[0].toPR(repo), true, nil
}

func (c *Client) checks(ctx context.Context, repo, sha string) (string, error) {
	if sha == "" {
		return "", nil
	}
	var r struct {
		Total int `json:"total_count"`
		Runs  []struct {
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"check_runs"`
	}
	if err := c.get(ctx, fmt.Sprintf("/repos/%s/commits/%s/check-runs?per_page=100", repo, sha), &r); err != nil {
		return "", err
	}
	return summarizeChecks(len(r.Runs), func(i int) (string, string) { return r.Runs[i].Status, r.Runs[i].Conclusion }), nil
}

// summarizeChecks reduces check runs to one state: any failure fails,
// anything unfinished is pending, else success.
func summarizeChecks(n int, run func(int) (status, conclusion string)) string {
	if n == 0 {
		return ""
	}
	state := "success"
	for i := 0; i < n; i++ {
		status, conclusion := run(i)
		switch {
		case status != "completed":
			if state == "success" {
				state = "pending"
			}
		case conclusion == "failure" || conclusion == "timed_out" || conclusion == "cancelled" || conclusion == "action_required" || conclusion == "startup_failure":
			return "failure"
		}
	}
	return state
}

func (c *Client) review(ctx context.Context, repo string, number int) (string, error) {
	var reviews []struct {
		State string `json:"state"`
		User  struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := c.get(ctx, fmt.Sprintf("/repos/%s/pulls/%d/reviews?per_page=100", repo, number), &reviews); err != nil {
		return "", err
	}
	// Each reviewer's latest approving or blocking review counts.
	latest := map[string]string{}
	for _, r := range reviews {
		if r.State == "APPROVED" || r.State == "CHANGES_REQUESTED" || r.State == "DISMISSED" {
			latest[r.User.Login] = r.State
		}
	}
	decision := ""
	for _, s := range latest {
		switch s {
		case "CHANGES_REQUESTED":
			return "changes_requested", nil
		case "APPROVED":
			decision = "approved"
		}
	}
	return decision, nil
}

// ParseURL reads "https://github.com/owner/repo/pull/12".
func ParseURL(u string) (repo string, number int, ok bool) {
	rest, found := strings.CutPrefix(u, "https://github.com/")
	if !found {
		return "", 0, false
	}
	parts := strings.Split(strings.TrimSuffix(rest, "/"), "/")
	if len(parts) < 4 || parts[2] != "pull" {
		return "", 0, false
	}
	n, err := strconv.Atoi(parts[3])
	if err != nil || n <= 0 {
		return "", 0, false
	}
	return parts[0] + "/" + parts[1], n, true
}

// RepoOf returns "owner/repo" for a github.com project id
// ("github.com/owner/repo"), or false.
func RepoOf(projectID string) (string, bool) {
	rest, ok := strings.CutPrefix(projectID, "github.com/")
	if !ok || strings.Count(rest, "/") != 1 {
		return "", false
	}
	return rest, true
}
