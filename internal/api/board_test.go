// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

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
	"strings"
	"testing"
	"time"

	"github.com/elephaant/shiplino/internal/store"
	"github.com/elephaant/shiplino/pkg/engine"
	"github.com/elephaant/shiplino/pkg/projects"
)

const pid = "github.com/acme/api"

func boardServer(t *testing.T) *httptest.Server {
	t.Helper()
	fixed := time.Date(2026, 10, 9, 12, 0, 0, 0, time.Local) // Friday, sprint 1
	now = func() time.Time { return fixed }
	t.Cleanup(func() { now = time.Now })

	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	tx, _ := st.Begin(ctx)
	tx.PutProject(ctx, projects.Project{ID: pid, Name: "api", Kind: "remote", Remote: pid}, fixed.Add(-48*time.Hour))
	mk := func(id string, status engine.Status, ago time.Duration, cost float64) *engine.Session {
		at := fixed.Add(-ago)
		return &engine.Session{ID: id, RootID: id, Agent: "claude-code", Model: "claude-opus-5-5", Status: status, ProjectID: pid,
			Title: id, StartedAt: at, LastEventAt: at.Add(10 * time.Minute), BestCostUSD: cost, Files: []string{"/r/a.go"}, LinesAdded: 5}
	}
	waiting := mk("claude-code:w", engine.StatusWaiting, time.Hour, 0.2)
	waiting.NowDoing, waiting.WaitingSince = "Approve: git push", fixed.Add(-5*time.Minute)
	for _, s := range []*engine.Session{
		mk("claude-code:r", engine.StatusRunning, 2*time.Hour, 1.0),
		waiting,
		mk("claude-code:v", engine.StatusReview, 3*time.Hour, 0.5),
		mk("claude-code:d", engine.StatusDone, 4*time.Hour, 0.3),
		{ID: "claude-code:r/sub:x", RootID: "claude-code:r", ParentID: "claude-code:r", ActorType: "reviewer", Agent: "claude-code",
			Status: engine.StatusRunning, ProjectID: pid, StartedAt: fixed, LastEventAt: fixed},
	} {
		tx.PutSession(ctx, s)
	}
	tx.Commit()
	srv := httptest.NewServer(New(st, NewHub(), token, "test", log.New(io.Discard, "", 0)).Handler())
	t.Cleanup(srv.Close)
	return srv
}

func call(t *testing.T, srv *httptest.Server, method, path, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func columns(t *testing.T, srv *httptest.Server) map[string][]string {
	t.Helper()
	code, b := call(t, srv, "GET", "/api/v1/projects/"+url.PathEscape(pid)+"/board", "")
	if code != 200 {
		t.Fatalf("board: %d %v", code, b)
	}
	out := map[string][]string{}
	for _, c := range b["columns"].([]any) {
		col := c.(map[string]any)
		for _, card := range col["cards"].([]any) {
			out[col["id"].(string)] = append(out[col["id"].(string)], card.(map[string]any)["id"].(string))
		}
	}
	return out
}

func TestBoardFlow(t *testing.T) {
	srv := boardServer(t)
	cols := columns(t, srv)
	if len(cols["running"]) != 1 || cols["waiting"][0] != "claude-code:w" || cols["review"][0] != "claude-code:v" || cols["done"][0] != "claude-code:d" {
		t.Fatalf("initial board: %v", cols)
	}

	// Manual card lands in Backlog.
	code, card := call(t, srv, "POST", "/api/v1/projects/"+url.PathEscape(pid)+"/cards", `{"title":"Add rate limiting"}`)
	if code != 201 {
		t.Fatalf("create: %d %v", code, card)
	}
	manual := card["card_id"].(string)
	if cols = columns(t, srv); len(cols["backlog"]) != 1 {
		t.Fatalf("backlog: %v", cols)
	}

	// Drag the review card to Done: it's pinned there.
	if code, b := call(t, srv, "PATCH", "/api/v1/cards/"+url.PathEscape("claude-code:v"), `{"column":"done","position":1}`); code != 200 {
		t.Fatalf("pin: %d %v", code, b)
	}
	if cols = columns(t, srv); len(cols["review"]) != 0 || cols["done"][0] != "claude-code:v" {
		t.Fatalf("after pin: %v", cols)
	}
	// Auto cards can't be put in Running/Waiting by hand.
	if code, _ := call(t, srv, "PATCH", "/api/v1/cards/"+url.PathEscape("claude-code:v"), `{"column":"running"}`); code != 422 {
		t.Fatalf("forbidden move: %d", code)
	}
	// Unpin: back to following the agent.
	call(t, srv, "PATCH", "/api/v1/cards/"+url.PathEscape("claude-code:v"), `{"pinned":false}`)
	if cols = columns(t, srv); cols["review"][0] != "claude-code:v" {
		t.Fatalf("after unpin: %v", cols)
	}
	// Subagent rows aren't cards; unknown cards 404.
	if code, _ := call(t, srv, "PATCH", "/api/v1/cards/"+url.PathEscape("claude-code:r/sub:x"), `{"column":"done"}`); code != 404 {
		t.Fatalf("subagent as card: %d", code)
	}
	// Auto cards can't be deleted; manual ones can.
	if code, _ := call(t, srv, "DELETE", "/api/v1/cards/"+url.PathEscape("claude-code:d"), ""); code != 404 {
		t.Fatalf("delete auto: %d", code)
	}
	if code, _ := call(t, srv, "DELETE", "/api/v1/cards/"+url.PathEscape(manual), ""); code != 204 {
		t.Fatalf("delete manual: %d", code)
	}
}

func TestSprintReportAndOverview(t *testing.T) {
	srv := boardServer(t)
	code, rep := call(t, srv, "GET", "/api/v1/projects/"+url.PathEscape(pid)+"/sprints/1/report", "")
	if code != 200 || rep["done"].(float64) != 1 || rep["open"].(float64) != 3 || rep["cost_usd"].(float64) < 1.99 {
		t.Fatalf("report: %d %v", code, rep)
	}
	files := rep["top_files"].([]any)
	if len(files) != 1 || files[0].(map[string]any)["sessions"].(float64) != 4 {
		t.Fatalf("top files: %v", files)
	}
	code, ov := call(t, srv, "GET", "/api/v1/overview", "")
	if code != 200 {
		t.Fatalf("overview: %d", code)
	}
	needs := ov["needs_you"].([]any)
	if len(needs) != 1 || needs[0].(map[string]any)["message"] != "Approve: git push" {
		t.Fatalf("needs you: %v", needs)
	}
	row := ov["projects"].([]any)[0].(map[string]any)
	if row["columns"].(map[string]any)["running"].(float64) != 1 || row["running_subagents"].(float64) != 1 {
		t.Fatalf("overview row: %v", row)
	}
	if code, _ := call(t, srv, "GET", "/api/v1/projects/nope/board", ""); code != 404 {
		t.Fatalf("unknown project: %d", code)
	}
	if code, _ := call(t, srv, "GET", "/api/v1/projects/"+url.PathEscape(pid)+"/board?sprint=x", ""); code != 400 {
		t.Fatalf("bad sprint: %d", code)
	}
}
