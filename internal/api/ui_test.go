package api

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestServeUI(t *testing.T) {
	app := fstest.MapFS{
		"index.html":          {Data: []byte("<html>overview</html>")},
		"board/index.html":    {Data: []byte("<html>board</html>")},
		"404.html":            {Data: []byte("<html>not found</html>")},
		"_next/static/app.js": {Data: []byte("console.log(1)")},
		"favicon.ico":         {Data: []byte("ico")},
	}
	s := New(nil, NewHub(), token, "t", log.New(io.Discard, "", 0))
	h := s.serveUI(app)
	cases := []struct {
		path, body string
		code       int
		cookie     bool
	}{
		{"/", "overview", 200, true},
		{"/board/", "board", 200, true},
		{"/board", "board", 200, true},
		{"/_next/static/app.js", "console.log(1)", 200, false},
		{"/favicon.ico", "ico", 200, false},
		{"/nope/", "not found", 404, true},
		{"/../../etc/passwd", "not found", 404, true},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest("GET", c.path, nil))
		if rec.Code != c.code || !strings.Contains(rec.Body.String(), c.body) {
			t.Errorf("%s: %d %q", c.path, rec.Code, rec.Body.String())
		}
		if got := strings.Contains(rec.Header().Get("Set-Cookie"), cookieName+"="); got != c.cookie {
			t.Errorf("%s: cookie set = %v", c.path, got)
		}
	}
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("GET", "/_next/static/app.js", nil))
	if !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Error("static assets should be cacheable")
	}
}

func TestDevCORS(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	off := (&Server{}).devCORS(ok)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/sessions", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	off.ServeHTTP(rec, req)
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("CORS headers without dev mode")
	}

	on := (&Server{DevOrigin: "http://localhost:3000"}).devCORS(ok)
	rec = httptest.NewRecorder()
	on.ServeHTTP(rec, req)
	if rec.Header().Get("Access-Control-Allow-Origin") != "http://localhost:3000" || rec.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatalf("dev origin not allowed: %v", rec.Header())
	}
	evil := httptest.NewRequest("GET", "/api/v1/sessions", nil)
	evil.Header.Set("Origin", "https://evil.example.com")
	rec = httptest.NewRecorder()
	on.ServeHTTP(rec, evil)
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("other origins allowed in dev mode")
	}
	pre := httptest.NewRequest("OPTIONS", "/api/v1/cards/x", nil)
	pre.Header.Set("Origin", "http://localhost:3000")
	rec = httptest.NewRecorder()
	on.ServeHTTP(rec, pre)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight: %d", rec.Code)
	}
}
