package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/elephaant/shiplino/pkg/engine"
	"github.com/elephaant/shiplino/pkg/model"
)

var ctx = context.Background()

// fakeGitHub serves one repo, acme/api, with PR 7 on branch feat/x.
type fakeGitHub struct {
	merged   atomic.Bool
	requests atomic.Int32
	full     atomic.Int32 // requests answered with a body (not 304)
}

func (f *fakeGitHub) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("X-RateLimit-Remaining", "4000")
		var body any
		switch {
		case r.URL.Path == "/repos/acme/api/pulls/7":
			pr := map[string]any{"number": 7, "html_url": "https://github.com/acme/api/pull/7", "title": "Fix login", "state": "open", "draft": false,
				"updated_at": "2026-10-10T09:00:00Z", "head": map[string]any{"sha": "abc", "ref": "feat/x"}}
			if f.merged.Load() {
				pr["state"], pr["merged_at"] = "closed", "2026-10-10T10:00:00Z"
			}
			body = pr
		case r.URL.Path == "/repos/acme/api/pulls" && r.URL.Query().Get("head") == "acme:feat/x":
			body = []map[string]any{{"number": 7, "html_url": "https://github.com/acme/api/pull/7", "title": "Fix login", "state": "open",
				"updated_at": "2026-10-10T09:00:00Z", "head": map[string]any{"sha": "abc", "ref": "feat/x"}}}
		case r.URL.Path == "/repos/acme/api/pulls":
			body = []any{}
		case r.URL.Path == "/repos/acme/api/commits/abc/check-runs":
			body = map[string]any{"total_count": 2, "check_runs": []map[string]any{{"status": "completed", "conclusion": "success"}, {"status": "in_progress"}}}
		case r.URL.Path == "/repos/acme/api/pulls/7/reviews":
			body = []map[string]any{{"state": "CHANGES_REQUESTED", "user": map[string]any{"login": "a"}}, {"state": "APPROVED", "user": map[string]any{"login": "a"}}}
		default:
			t.Errorf("unexpected request %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		b, _ := json.Marshal(body)
		etag := fmt.Sprintf(`"%x"`, len(b)) + fmt.Sprint(f.merged.Load())
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		f.full.Add(1)
		w.Header().Set("ETag", etag)
		w.Write(b)
	})
}

func TestClient(t *testing.T) {
	f := &fakeGitHub{}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	c := New("tok")
	c.Base = srv.URL
	pr, err := c.Get(ctx, "acme/api", 7)
	if err != nil {
		t.Fatal(err)
	}
	// Latest review per reviewer wins (approved after changes requested);
	// one check still running means pending.
	if pr.State != "open" || pr.Checks != "pending" || pr.Review != "approved" || pr.Title != "Fix login" || pr.Branch != "feat/x" {
		t.Fatalf("pr: %+v", pr)
	}
	full := f.full.Load()
	if _, err := c.Get(ctx, "acme/api", 7); err != nil || f.full.Load() != full {
		t.Fatalf("unchanged PR wasn't served from the ETag cache (%d → %d)", full, f.full.Load())
	}
	if c.Remaining() != 4000 {
		t.Fatalf("remaining %d", c.Remaining())
	}
	if _, found, _ := c.ForBranch(ctx, "acme/api", "other"); found {
		t.Fatal("found a PR for a branch without one")
	}
	bad := New("nope")
	bad.Base = srv.URL
	if _, err := bad.Get(ctx, "acme/api", 7); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("bad token: %v", err)
	}
}

func TestSummarizeChecks(t *testing.T) {
	cases := []struct {
		runs [][2]string
		want string
	}{
		{nil, ""},
		{[][2]string{{"completed", "success"}, {"completed", "skipped"}}, "success"},
		{[][2]string{{"completed", "success"}, {"queued", ""}}, "pending"},
		{[][2]string{{"queued", ""}, {"completed", "failure"}}, "failure"},
		{[][2]string{{"completed", "timed_out"}}, "failure"},
	}
	for _, c := range cases {
		got := summarizeChecks(len(c.runs), func(i int) (string, string) { return c.runs[i][0], c.runs[i][1] })
		if got != c.want {
			t.Errorf("%v: %q, want %q", c.runs, got, c.want)
		}
	}
}

func TestParse(t *testing.T) {
	if repo, n, ok := ParseURL("https://github.com/acme/api/pull/12"); !ok || repo != "acme/api" || n != 12 {
		t.Fatal(repo, n, ok)
	}
	for _, bad := range []string{"https://gitlab.com/a/b/pull/1", "https://github.com/acme/api/issues/3", "https://github.com/acme/api/pull/x"} {
		if _, _, ok := ParseURL(bad); ok {
			t.Errorf("%s parsed", bad)
		}
	}
	if repo, ok := RepoOf("github.com/acme/api"); !ok || repo != "acme/api" {
		t.Fatal(repo)
	}
	if _, ok := RepoOf("gitlab.com/acme/api"); ok {
		t.Fatal("non-GitHub project")
	}
}

type fakeStore struct {
	sessions []*engine.Session
	prs      map[string]PR
}

func (f *fakeStore) SessionsActiveSince(context.Context, time.Time) ([]*engine.Session, error) {
	return f.sessions, nil
}
func (f *fakeStore) PRs(context.Context) (map[string]PR, error) { return f.prs, nil }
func (f *fakeStore) PutPR(_ context.Context, pr PR) error       { f.prs[pr.URL] = pr; return nil }

func TestPoller(t *testing.T) {
	f := &fakeGitHub{}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	now := time.Now()
	byBranch := &engine.Session{ID: "cc:1", Agent: "claude-code", ProjectID: "github.com/acme/api", Branch: "feat/x", LastEventAt: now}
	onMain := &engine.Session{ID: "cc:2", Agent: "claude-code", ProjectID: "github.com/acme/api", Branch: "main", LastEventAt: now}
	st := &fakeStore{sessions: []*engine.Session{byBranch, onMain}, prs: map[string]PR{}}
	var recorded []model.Event
	var refreshed int
	p := NewPoller(st, func(_ context.Context, evs []model.Event) error { recorded = append(recorded, evs...); return nil },
		func(l []*engine.Session) { refreshed += len(l) }, time.Minute)
	p.newToken = func(context.Context) (string, string, error) { return "tok", "test", nil }
	p.base = srv.URL
	p.Poll(ctx)

	if s := p.Status(); s.Error != "" || s.Tracked != 1 || s.TokenSource != "test" {
		t.Fatalf("status: %+v", s)
	}
	if len(recorded) != 1 || recorded[0].Data["action"] != "linked" || recorded[0].SessionID != "cc:1" || recorded[0].DedupKey != "cc:1:pr:7" {
		t.Fatalf("branch link: %+v", recorded)
	}
	if st.prs["https://github.com/acme/api/pull/7"].State != "open" || refreshed != 1 {
		t.Fatalf("stored: %+v refreshed %d", st.prs, refreshed)
	}

	// The PR merges: one "merged" event, stored state updated.
	byBranch.Links = []engine.Link{{Kind: "pr", URL: "https://github.com/acme/api/pull/7", Number: 7}}
	f.merged.Store(true)
	p.Poll(ctx)
	if st.prs["https://github.com/acme/api/pull/7"].State != "merged" || len(recorded) != 2 || recorded[1].Data["action"] != "merged" || recorded[1].DedupKey != "cc:1:pr:7:merged" {
		t.Fatalf("merge: %+v %+v", st.prs, recorded)
	}
	// Merged PRs aren't polled again for a day.
	n := f.requests.Load()
	p.Poll(ctx)
	if f.requests.Load() != n {
		t.Fatalf("merged PR polled again: %d requests", f.requests.Load()-n)
	}
}

func TestPollerWithoutToken(t *testing.T) {
	p := NewPoller(&fakeStore{prs: map[string]PR{}}, nil, nil, time.Minute)
	p.newToken = func(context.Context) (string, string, error) { return "", "", ErrNoToken }
	p.Poll(ctx)
	if s := p.Status(); s.Error == "" || !strings.Contains(s.Error, "gh auth login") {
		t.Fatalf("status: %+v", s)
	}
}
