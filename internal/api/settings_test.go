package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type fakeAdmin struct {
	paused   bool
	until    time.Time
	notified int
	changes  []string
	fail     bool
}

func (a *fakeAdmin) Settings(context.Context) any {
	return map[string]any{"paused": a.paused, "until": a.until}
}
func (a *fakeAdmin) Pause(u time.Time) error { a.paused, a.until = true, u; return nil }
func (a *fakeAdmin) Resume() error           { a.paused = false; return nil }
func (a *fakeAdmin) Backfill(since time.Time) int {
	a.until = since
	return 3
}
func (a *fakeAdmin) AgentPreview(_ context.Context, key string, connect bool) (any, error) {
	if key != "claude-code" {
		return nil, ErrUnknownAgent
	}
	return map[string]any{"key": key, "connect": connect, "diff": "+<script>"}, nil
}
func (a *fakeAdmin) AgentChange(_ context.Context, key string, connect bool) error {
	switch key {
	case "claude-code":
		a.changes = append(a.changes, fmt.Sprint(key, " ", connect))
		return nil
	case "cursor":
		return errors.New("~/.cursor/hooks.json isn't plain JSON (comments?), so Shiplino left it untouched")
	}
	return ErrUnknownAgent
}
func (a *fakeAdmin) TestNotification(context.Context) error {
	if a.fail {
		return errors.New("no notification service found")
	}
	a.notified++
	return nil
}

func (f *fixture) post(t *testing.T, path, body string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", f.url+path, strings.NewReader(body))
	bearer(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestSettingsEndpoints(t *testing.T) {
	f := setup(t)
	if resp, _ := f.get(t, "/api/v1/settings", bearer); resp.StatusCode != 501 {
		t.Fatalf("without admin: %d", resp.StatusCode)
	}
	a := &fakeAdmin{}
	f.s.Admin = a
	if resp, _ := f.get(t, "/api/v1/settings", nil); resp.StatusCode != 401 {
		t.Fatalf("settings without auth: %d", resp.StatusCode)
	}
	if resp, _ := f.get(t, "/api/v1/settings", bearer); resp.StatusCode != 200 {
		t.Fatalf("settings: %d", resp.StatusCode)
	}
	if resp, _ := f.post(t, "/api/v1/pause", `{"minutes":30}`); resp.StatusCode != 200 || !a.paused || time.Until(a.until) < 29*time.Minute {
		t.Fatalf("pause 30m: %d %+v", resp.StatusCode, a)
	}
	if resp, _ := f.post(t, "/api/v1/pause", ""); resp.StatusCode != 200 || !a.until.IsZero() {
		t.Fatalf("pause until resumed: %d %+v", resp.StatusCode, a)
	}
	if resp, _ := f.post(t, "/api/v1/pause", `{"minutes":-1}`); resp.StatusCode != 400 {
		t.Fatalf("bad minutes: %d", resp.StatusCode)
	}
	if resp, _ := f.post(t, "/api/v1/resume", ""); resp.StatusCode != 200 || a.paused {
		t.Fatalf("resume: %d", resp.StatusCode)
	}
	if resp, body := f.post(t, "/api/v1/backfill", `{"days":7}`); resp.StatusCode != 200 || !strings.Contains(body, `"transcripts":3`) || time.Since(a.until) < 6*24*time.Hour {
		t.Fatalf("backfill: %d %s", resp.StatusCode, body)
	}
	if resp, _ := f.post(t, "/api/v1/backfill", `{"days":0}`); resp.StatusCode != 400 {
		t.Fatalf("bad days: %d", resp.StatusCode)
	}
	if resp, _ := f.post(t, "/api/v1/notify/test", ""); resp.StatusCode != 204 || a.notified != 1 {
		t.Fatalf("notify test: %d", resp.StatusCode)
	}
	a.fail = true
	if resp, body := f.post(t, "/api/v1/notify/test", ""); resp.StatusCode != 502 || !strings.Contains(body, "no notification service") {
		t.Fatalf("notify failure: %d %s", resp.StatusCode, body)
	}
}

func TestAgentEndpoints(t *testing.T) {
	f := setup(t)
	if resp, _ := f.get(t, "/api/v1/agents/claude-code/preview?action=connect", bearer); resp.StatusCode != 501 {
		t.Fatalf("without admin: %d", resp.StatusCode)
	}
	a := &fakeAdmin{}
	f.s.Admin = a

	// Preview: authenticated, read-only, JSON (the UI renders it as text).
	if resp, _ := f.get(t, "/api/v1/agents/claude-code/preview?action=connect", nil); resp.StatusCode != 401 {
		t.Fatalf("preview without auth: %d", resp.StatusCode)
	}
	resp, body := f.get(t, "/api/v1/agents/claude-code/preview?action=remove", bearer)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/json" || !strings.Contains(string(body), `"connect":false`) || !strings.Contains(string(body), "\\u003cscript\\u003e") {
		t.Fatalf("preview: %d %q %s", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	if resp, _ := f.get(t, "/api/v1/agents/claude-code/preview?action=wipe", bearer); resp.StatusCode != 400 {
		t.Fatalf("bad action: %d", resp.StatusCode)
	}
	if resp, _ := f.get(t, "/api/v1/agents/nope/preview?action=connect", bearer); resp.StatusCode != 404 {
		t.Fatalf("unknown agent: %d", resp.StatusCode)
	}

	// Connect and remove need the token or the UI cookie...
	req, _ := http.NewRequest("POST", f.url+"/api/v1/agents/claude-code/connect", nil)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 401 {
		t.Fatalf("connect without auth: %v %v", resp.StatusCode, err)
	}
	if resp, body := f.post(t, "/api/v1/agents/claude-code/connect", ""); resp.StatusCode != 200 || !strings.Contains(body, "paused") {
		t.Fatalf("connect: %d %s", resp.StatusCode, body)
	}
	if resp, _ := f.post(t, "/api/v1/agents/claude-code/remove", ""); resp.StatusCode != 200 {
		t.Fatalf("remove: %d", resp.StatusCode)
	}
	if fmt.Sprint(a.changes) != "[claude-code true claude-code false]" {
		t.Fatalf("changes: %v", a.changes)
	}
	if resp, body := f.post(t, "/api/v1/agents/cursor/connect", ""); resp.StatusCode != 409 || !strings.Contains(body, "left it untouched") {
		t.Fatalf("unparseable config: %d %s", resp.StatusCode, body)
	}
	if resp, _ := f.post(t, "/api/v1/agents/nope/remove", ""); resp.StatusCode != 404 {
		t.Fatalf("unknown agent: %d", resp.StatusCode)
	}

	// ...and come from the page itself: another origin (even another
	// localhost port, which the SameSite cookie doesn't stop) is refused.
	for origin, want := range map[string]int{
		"":                      200,
		f.url:                   200,
		"http://localhost:8080": 403,
		"https://evil.example":  403,
		"null":                  403,
		strings.ToUpper(f.url):  403,
	} {
		req, _ := http.NewRequest("POST", f.url+"/api/v1/agents/claude-code/remove", nil)
		req.AddCookie(&http.Cookie{Name: cookieName, Value: token})
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("Origin %q: %d, want %d", origin, resp.StatusCode, want)
		}
	}
	f.s.DevOrigin = "http://localhost:3000"
	req, _ = http.NewRequest("POST", f.url+"/api/v1/agents/claude-code/remove", nil)
	bearer(req)
	req.Header.Set("Origin", "http://localhost:3000")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 200 {
		t.Fatalf("dev origin: %v %v", resp.StatusCode, err)
	}
}
