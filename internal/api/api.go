// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/elephaant/shiplino/internal/store"
	"github.com/elephaant/shiplino/pkg/engine"
	"github.com/elephaant/shiplino/pkg/model"
)

// DefaultPort is the first port tried; up to 10 more follow if it's busy.
const DefaultPort = 4777

const cookieName = "shiplino_token"

//go:embed index.html
var indexHTML []byte

// Server is the local REST + WebSocket API and the placeholder web page.
type Server struct {
	st      *store.Store
	token   string
	hub     *Hub
	log     *log.Logger
	version string

	// Status, if set, reports daemon health for /api/v1/status.
	Status func() any
	// DevOrigin, if set (e.g. "http://localhost:3000"), is the one extra
	// origin allowed to call the API with credentials, for `next dev`.
	// Empty in normal use: the API is strictly same-origin.
	DevOrigin string
}

// New returns a server reading from st and pushing hub updates.
func New(st *store.Store, hub *Hub, token, version string, logger *log.Logger) *Server {
	if logger == nil {
		logger = log.New(os.Stderr, "", 0)
	}
	return &Server{st: st, token: token, hub: hub, log: logger, version: version}
}

// Handler returns the full HTTP handler with security checks applied.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	if app, ok := webApp(); ok {
		mux.HandleFunc("GET /", s.serveUI(app))
	} else {
		mux.HandleFunc("GET /{$}", s.index)
	}
	mux.HandleFunc("GET /api/v1/health", s.health)
	mux.Handle("GET /api/v1/sessions", s.auth(s.listSessions))
	mux.Handle("GET /api/v1/projects", s.auth(s.listProjects))
	mux.Handle("GET /api/v1/projects/{id}/board", s.auth(s.getBoard))
	mux.Handle("POST /api/v1/projects/{id}/cards", s.auth(s.createCard))
	mux.Handle("GET /api/v1/projects/{id}/sprints/{n}/report", s.auth(s.sprintReport))
	mux.Handle("PATCH /api/v1/cards/{id}", s.auth(s.patchCard))
	mux.Handle("DELETE /api/v1/cards/{id}", s.auth(s.deleteCard))
	mux.Handle("GET /api/v1/overview", s.auth(s.overview))
	mux.Handle("GET /api/v1/sessions/{id}", s.auth(s.getSession))
	mux.Handle("GET /api/v1/sessions/{id}/events", s.auth(s.listEvents))
	mux.Handle("GET /api/v1/live", s.auth(s.live))
	mux.Handle("GET /api/v1/status", s.auth(s.status))
	return securityHeaders(localHostOnly(s.devCORS(mux)))
}

// devCORS allows exactly DevOrigin, and only when it's configured.
func (s *Server) devCORS(next http.Handler) http.Handler {
	if s.DevOrigin == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") == s.DevOrigin {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", s.DevOrigin)
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			h.Add("Vary", "Origin")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// localHostOnly rejects requests whose Host isn't loopback. This blocks
// DNS-rebinding attacks from web pages the user visits.
func localHostOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		switch strings.Trim(host, "[]") {
		case "localhost", "127.0.0.1", "::1":
			next.ServeHTTP(w, r)
		default:
			http.Error(w, "forbidden host", http.StatusForbidden)
		}
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; frame-ancestors 'none'")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// auth requires the local token as a Bearer header or the UI cookie.
func (s *Server) auth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got string
		if h := r.Header.Get("Authorization"); h != "" {
			got, _ = strings.CutPrefix(h, "Bearer ")
			if got == h {
				got = "" // not a Bearer header: reject rather than guess
			}
		} else if c, err := r.Cookie(cookieName); err == nil {
			got = c.Value
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			writeError(w, http.StatusUnauthorized, "missing or invalid token")
			return
		}
		next(w, r)
	})
}

// index serves the page and hands the browser the token as a strict,
// HttpOnly cookie, so page scripts never see the token itself.
func (s *Server) setCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: s.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	s.setCookie(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(indexHTML)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": s.version})
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"version": s.version}
	if s.Status != nil {
		out["daemon"] = s.Status()
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	limit := 200
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			writeError(w, http.StatusBadRequest, "limit must be 1-1000")
			return
		}
		limit = n
	}
	list, err := s.st.SessionsIn(r.Context(), r.URL.Query().Get("project"), limit)
	if err != nil {
		s.internal(w, err)
		return
	}
	if list == nil {
		list = []*engine.Session{} // encode as [] rather than null
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": list})
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.Projects(r.Context())
	if err != nil {
		s.internal(w, err)
		return
	}
	if list == nil {
		list = []store.ProjectSummary{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": list})
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	sess, err := s.st.Session(r.Context(), r.PathValue("id"))
	if err != nil {
		s.internal(w, err)
		return
	}
	if sess == nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 500
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			writeError(w, http.StatusBadRequest, "limit must be 1-1000")
			return
		}
		limit = n
	}
	evs, err := s.st.Events(r.Context(), rootOf(r.PathValue("id")), q.Get("after"), limit)
	if err != nil {
		s.internal(w, err)
		return
	}
	if evs == nil {
		evs = []model.Event{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": evs})
}

// rootOf maps a subagent id ("<session>/sub:<id>") to its session; events
// are stored under the top-level session id.
func rootOf(id string) string {
	if i := strings.Index(id, "/sub:"); i >= 0 {
		return id[:i]
	}
	return id
}

func (s *Server) internal(w http.ResponseWriter, err error) {
	s.log.Printf("api: %v", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// LoadToken reads home/token, creating a random 256-bit token (0600) if
// it doesn't exist yet.
func LoadToken(home string) (string, error) {
	path := filepath.Join(home, "token")
	if b, err := os.ReadFile(path); err == nil {
		if t := strings.TrimSpace(string(b)); len(t) >= 32 {
			return t, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	t := hex.EncodeToString(buf)
	if err := os.MkdirAll(home, 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(t+"\n"), 0o600); err != nil {
		return "", err
	}
	return t, nil
}

// Listen binds 127.0.0.1 on the first free port in [base, base+10] and
// records it in home/port.
func Listen(home string, base int) (net.Listener, error) {
	var lastErr error
	for p := base; p <= base+10; p++ {
		ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p)))
		if err != nil {
			lastErr = err
			continue
		}
		if err := os.WriteFile(filepath.Join(home, "port"), []byte(strconv.Itoa(p)+"\n"), 0o600); err != nil {
			ln.Close()
			return nil, err
		}
		return ln, nil
	}
	return nil, fmt.Errorf("no free port in %d-%d: %w", base, base+10, lastErr)
}

// Serve runs the server on ln until ctx is cancelled.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
