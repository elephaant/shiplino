package api

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/elephaant/shiplino/internal/integrations/github"
	"github.com/elephaant/shiplino/internal/store"
	"github.com/elephaant/shiplino/pkg/engine"
	"github.com/elephaant/shiplino/pkg/projects"
)

// With the GitHub integration's state stored, cards and sessions carry it
// on their PR links.
func TestPRStateOnCardsAndSessions(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	at := time.Now().Add(-time.Hour)
	const prURL = "https://github.com/acme/api/pull/7"
	tx, _ := st.Begin(ctx)
	tx.PutProject(ctx, projects.Project{ID: pid, Name: "api", Kind: "remote", Remote: pid}, at)
	tx.PutSession(ctx, &engine.Session{ID: "claude-code:p", RootID: "claude-code:p", Agent: "claude-code", Status: engine.StatusReview,
		ProjectID: pid, StartedAt: at, LastEventAt: at, Links: []engine.Link{{Kind: "pr", URL: prURL, Number: 7}}})
	tx.Commit()
	st.PutPR(ctx, github.PR{URL: prURL, Repo: "acme/api", Number: 7, State: "open", Checks: "failure", Review: "approved", Title: "Fix login"})

	srv := httptest.NewServer(New(st, NewHub(), token, "test", log.New(io.Discard, "", 0)).Handler())
	defer srv.Close()
	get := func(path string, v any) {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("%s: %v %v", path, resp, err)
		}
		defer resp.Body.Close()
		json.NewDecoder(resp.Body).Decode(v)
	}
	var board struct {
		Columns []struct {
			Cards []struct{ Links []engine.Link }
		}
	}
	get("/api/v1/projects/"+url.PathEscape(pid)+"/board", &board)
	var found *engine.Link
	for _, c := range board.Columns {
		for _, card := range c.Cards {
			for i := range card.Links {
				found = &card.Links[i]
			}
		}
	}
	if found == nil || found.State != "open" || found.Checks != "failure" || found.Review != "approved" || found.Title != "Fix login" {
		t.Fatalf("board link: %+v", found)
	}
	var sess engine.Session
	get("/api/v1/sessions/claude-code:p", &sess)
	if len(sess.Links) != 1 || sess.Links[0].Checks != "failure" {
		t.Fatalf("session links: %+v", sess.Links)
	}
	// The session also says how its project was found (evidence badges).
	var kind struct {
		ProjectKind string `json:"project_kind"`
	}
	if get("/api/v1/sessions/claude-code:p", &kind); kind.ProjectKind != "remote" {
		t.Fatalf("project_kind = %q, want remote", kind.ProjectKind)
	}
}
