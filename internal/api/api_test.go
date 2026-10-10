package api

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/elephaant/shiplino/internal/store"
	"github.com/elephaant/shiplino/pkg/engine"
	"github.com/elephaant/shiplino/pkg/model"
)

const token = "test-token-0123456789abcdef0123456789abcdef"

type fixture struct {
	s   *Server
	srv *httptest.Server
	hub *Hub
	url string // http://127.0.0.1:port
}

func setup(t *testing.T) *fixture {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "shiplino.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	tx, _ := st.Begin(ctx)
	root := &engine.Session{ID: "claude-code:s1", RootID: "claude-code:s1", Agent: "claude-code", Status: engine.StatusRunning, StartedAt: now, LastEventAt: now, Title: "Fix login"}
	child := &engine.Session{ID: "claude-code:s1/sub:a1", RootID: "claude-code:s1", ParentID: "claude-code:s1", Agent: "claude-code", Status: engine.StatusDone, StartedAt: now, LastEventAt: now}
	tx.PutSession(ctx, root)
	tx.PutSession(ctx, child)
	tx.InsertEvent(ctx, model.Event{ID: model.NewULID(now), V: 1, TS: now, Kind: model.KindTurnStart, Agent: model.Agent{Name: "claude-code"},
		Collector: model.CollectorHook, SessionID: "claude-code:s1", DedupKey: "k1", Data: map[string]any{"prompt": "fix the login redirect"}})
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	hub := NewHub()
	hctx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go hub.Run(hctx)

	s := New(st, hub, token, "test", log.New(io.Discard, "", 0))
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return &fixture{s: s, srv: srv, hub: hub, url: srv.URL}
}

func (f *fixture) get(t *testing.T, path string, mod func(*http.Request)) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest("GET", f.url+path, nil)
	if mod != nil {
		mod(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func bearer(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }

func TestLimits(t *testing.T) {
	f := setup(t)
	if resp, _ := f.get(t, "/api/v1/limits", bearer); resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("without limits: %d", resp.StatusCode)
	}
	if resp, _ := f.get(t, "/api/v1/limits", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d", resp.StatusCode)
	}
	f.s.Limits = func(context.Context) (any, error) { return map[string]any{"windows": []any{}}, nil }
	resp, body := f.get(t, "/api/v1/limits", bearer)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"windows":[]`) {
		t.Fatalf("limits: %d %s", resp.StatusCode, body)
	}
}

func TestStatusReportsDemo(t *testing.T) {
	f := setup(t)
	if _, body := f.get(t, "/api/v1/status", bearer); strings.Contains(string(body), `"demo"`) {
		t.Fatalf("a normal daemon reports demo: %s", body)
	}
	f.s.Demo = true
	if _, body := f.get(t, "/api/v1/status", bearer); !strings.Contains(string(body), `"demo":true`) {
		t.Fatalf("status = %s", body)
	}
}

func TestListenAnyPort(t *testing.T) {
	home := t.TempDir()
	ln, err := Listen(home, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	b, _ := os.ReadFile(filepath.Join(home, "port"))
	if got := ln.Addr().(*net.TCPAddr).Port; got == 0 || strings.TrimSpace(string(b)) != strconv.Itoa(got) {
		t.Fatalf("listening on %d, port file %q", got, b)
	}
}

func TestHostHeaderBlocksDNSRebinding(t *testing.T) {
	f := setup(t)
	for _, host := range []string{"evil.example.com", "evil.example.com:4777", "192.168.1.5"} {
		resp, _ := f.get(t, "/api/v1/health", func(r *http.Request) { r.Host = host })
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("Host %q: status %d, want 403", host, resp.StatusCode)
		}
	}
	for _, host := range []string{"localhost:4777", "127.0.0.1:4777", "[::1]:4777"} {
		resp, _ := f.get(t, "/api/v1/health", func(r *http.Request) { r.Host = host })
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Host %q: status %d, want 200", host, resp.StatusCode)
		}
	}
}

func TestAuthRequired(t *testing.T) {
	f := setup(t)
	cases := map[string]func(*http.Request){
		"none":         nil,
		"wrong bearer": func(r *http.Request) { r.Header.Set("Authorization", "Bearer nope") },
		"no prefix":    func(r *http.Request) { r.Header.Set("Authorization", token) },
		"wrong cookie": func(r *http.Request) { r.AddCookie(&http.Cookie{Name: cookieName, Value: "nope"}) },
	}
	for name, mod := range cases {
		if resp, _ := f.get(t, "/api/v1/sessions", mod); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, resp.StatusCode)
		}
	}
}

func TestIndexSetsStrictCookieThatAuthenticates(t *testing.T) {
	f := setup(t)
	resp, body := f.get(t, "/", nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "<title>Shiplino</title>") {
		t.Fatalf("index: %d", resp.StatusCode)
	}
	var c *http.Cookie
	for _, ck := range resp.Cookies() {
		if ck.Name == cookieName {
			c = ck
		}
	}
	if c == nil || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Value != token {
		t.Fatalf("cookie = %+v", c)
	}
	if got := resp.Header.Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q", got)
	}
	if strings.Contains(string(body), token) {
		t.Fatal("token leaked into the page body")
	}
	r2, _ := f.get(t, "/api/v1/sessions", func(r *http.Request) { r.AddCookie(c) })
	if r2.StatusCode != 200 {
		t.Fatalf("cookie auth: %d", r2.StatusCode)
	}
}

func TestNoCORSHeaders(t *testing.T) {
	f := setup(t)
	resp, _ := f.get(t, "/api/v1/sessions", func(r *http.Request) {
		bearer(r)
		r.Header.Set("Origin", "https://evil.example.com")
	})
	if v := resp.Header.Get("Access-Control-Allow-Origin"); v != "" {
		t.Fatalf("CORS header present: %q", v)
	}
}

func TestSessionsAndEvents(t *testing.T) {
	f := setup(t)
	resp, body := f.get(t, "/api/v1/sessions", bearer)
	var list struct{ Sessions []engine.Session }
	if resp.StatusCode != 200 || json.Unmarshal(body, &list) != nil || len(list.Sessions) != 2 {
		t.Fatalf("list: %d %s", resp.StatusCode, body)
	}

	resp, body = f.get(t, "/api/v1/sessions/"+url.PathEscape("claude-code:s1/sub:a1"), bearer)
	var one engine.Session
	if resp.StatusCode != 200 || json.Unmarshal(body, &one) != nil || one.ParentID != "claude-code:s1" {
		t.Fatalf("subagent detail: %d %s", resp.StatusCode, body)
	}
	if strings.Contains(string(body), "project_kind") {
		t.Fatalf("no project, but project_kind is set: %s", body)
	}
	if resp, _ := f.get(t, "/api/v1/sessions/nope", bearer); resp.StatusCode != 404 {
		t.Fatalf("missing session: %d", resp.StatusCode)
	}

	resp, body = f.get(t, "/api/v1/sessions/"+url.PathEscape("claude-code:s1/sub:a1")+"/events", bearer)
	var evs struct{ Events []model.Event }
	if resp.StatusCode != 200 || json.Unmarshal(body, &evs) != nil || len(evs.Events) != 1 {
		t.Fatalf("events: %d %s", resp.StatusCode, body)
	}
	resp, body = f.get(t, "/api/v1/projects", bearer)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"projects":[]`) {
		t.Fatalf("projects: %d %s", resp.StatusCode, body)
	}
	if resp, _ := f.get(t, "/api/v1/sessions?limit=0", bearer); resp.StatusCode != 400 {
		t.Fatalf("bad limit: %d", resp.StatusCode)
	}
	resp, body = f.get(t, "/api/v1/sessions/claude-code:none/events", bearer)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"events":[]`) {
		t.Fatalf("empty events: %s", body)
	}
}

func dial(t *testing.T, f *fixture, origin string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	h := http.Header{}
	h.Set("Authorization", "Bearer "+token)
	if origin != "" {
		h.Set("Origin", origin)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return websocket.Dial(ctx, "ws"+strings.TrimPrefix(f.url, "http")+"/api/v1/live", &websocket.DialOptions{HTTPHeader: h})
}

func TestLiveCoalescesUpdates(t *testing.T) {
	f := setup(t)
	conn, _, err := dial(t, f, "")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, b, err := conn.Read(ctx)
	if err != nil || !strings.Contains(string(b), `"hello"`) {
		t.Fatalf("hello: %s %v", b, err)
	}
	s := &engine.Session{ID: "claude-code:s9", Status: engine.StatusRunning}
	for i := 0; i < 50; i++ { // a burst of updates to one session…
		s.ToolCalls = i
		f.hub.Publish([]*engine.Session{s})
	}
	_, b, err = conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var m Message
	if err := json.Unmarshal(b, &m); err != nil || m.T != "session.update" || m.Session.ToolCalls != 49 {
		t.Fatalf("…arrives as one message with the latest state: %s", b)
	}
	readCtx, cancel2 := context.WithTimeout(ctx, 3*flushEvery)
	defer cancel2()
	if _, extra, err := conn.Read(readCtx); err == nil {
		t.Fatalf("unexpected extra message: %s", extra)
	}
}

func TestLiveRejectsCrossOrigin(t *testing.T) {
	f := setup(t)
	if conn, resp, err := dial(t, f, "https://evil.example.com"); err == nil {
		conn.CloseNow()
		t.Fatal("cross-origin WebSocket accepted")
	} else if resp != nil && resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, want 403", resp.StatusCode)
	}
}

func TestLoadToken(t *testing.T) {
	home := t.TempDir()
	a, err := LoadToken(home)
	if err != nil || len(a) != 64 {
		t.Fatalf("token %q, %v", a, err)
	}
	b, _ := LoadToken(home)
	if a != b {
		t.Fatal("token not stable across loads")
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(filepath.Join(home, "token"))
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("token perm = %v", fi.Mode().Perm())
		}
	}
}

func TestListenFallsBackToNextPort(t *testing.T) {
	home := t.TempDir()
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	base := busy.Addr().(*net.TCPAddr).Port
	ln, err := Listen(home, base)
	if err != nil {
		t.Skipf("no free port near %d: %v", base, err)
	}
	defer ln.Close()
	got := ln.Addr().(*net.TCPAddr)
	if got.Port == base || !got.IP.IsLoopback() {
		t.Fatalf("listened on %v", got)
	}
	b, _ := os.ReadFile(filepath.Join(home, "port"))
	if strings.TrimSpace(string(b)) != strconv.Itoa(got.Port) {
		t.Fatalf("port file = %q", b)
	}
}
