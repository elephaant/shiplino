package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// Admin is what the settings page reads and changes. The daemon
// implements it; nil disables these endpoints.
type Admin interface {
	Settings(ctx context.Context) any
	Pause(until time.Time) error // zero: until resumed
	Resume() error
	TestNotification(ctx context.Context) error
	// Backfill imports transcripts modified since `since` and returns how
	// many files were added.
	Backfill(since time.Time) int
	// AgentPreview shows the diff connecting (or removing) an agent's
	// hooks would make, writing nothing. key is agents.Hooks.Key.
	AgentPreview(ctx context.Context, key string, connect bool) (any, error)
	// AgentChange connects or removes an agent's hooks, as setup and
	// uninstall do.
	AgentChange(ctx context.Context, key string, connect bool) error
}

// ErrUnknownAgent is returned by Admin for an agent key it doesn't know.
var ErrUnknownAgent = errors.New("unknown agent")

func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	if s.Admin == nil {
		writeError(w, http.StatusNotImplemented, "settings unavailable")
		return
	}
	writeJSON(w, http.StatusOK, s.Admin.Settings(r.Context()))
}

// pause: POST /api/v1/pause {"minutes": 60}; 0 or no body pauses until resumed.
func (s *Server) pause(w http.ResponseWriter, r *http.Request) {
	if s.Admin == nil {
		writeError(w, http.StatusNotImplemented, "settings unavailable")
		return
	}
	var body struct {
		Minutes int `json:"minutes"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil || body.Minutes < 0 || body.Minutes > 7*24*60 {
			writeError(w, http.StatusBadRequest, "minutes must be 0-10080")
			return
		}
	}
	var until time.Time
	if body.Minutes > 0 {
		until = time.Now().Add(time.Duration(body.Minutes) * time.Minute)
	}
	if err := s.Admin.Pause(until); err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.Admin.Settings(r.Context()))
}

func (s *Server) resume(w http.ResponseWriter, r *http.Request) {
	if s.Admin == nil {
		writeError(w, http.StatusNotImplemented, "settings unavailable")
		return
	}
	if err := s.Admin.Resume(); err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.Admin.Settings(r.Context()))
}

// backfill: POST /api/v1/backfill {"days": 30}
func (s *Server) backfill(w http.ResponseWriter, r *http.Request) {
	if s.Admin == nil {
		writeError(w, http.StatusNotImplemented, "settings unavailable")
		return
	}
	body := struct {
		Days int `json:"days"`
	}{Days: 30}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil || body.Days < 1 || body.Days > 3650 {
			writeError(w, http.StatusBadRequest, "days must be 1-3650")
			return
		}
	}
	n := s.Admin.Backfill(time.Now().AddDate(0, 0, -body.Days))
	writeJSON(w, http.StatusOK, map[string]int{"transcripts": n, "days": body.Days})
}

// agentPreview: GET /api/v1/agents/{key}/preview?action=connect|remove
func (s *Server) agentPreview(w http.ResponseWriter, r *http.Request) {
	if s.Admin == nil {
		writeError(w, http.StatusNotImplemented, "settings unavailable")
		return
	}
	var connect bool
	switch r.URL.Query().Get("action") {
	case "connect":
		connect = true
	case "remove":
	default:
		writeError(w, http.StatusBadRequest, "action must be connect or remove")
		return
	}
	v, err := s.Admin.AgentPreview(r.Context(), r.PathValue("key"), connect)
	if err != nil {
		agentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// agentChange: POST /api/v1/agents/{key}/connect or /remove. It edits
// the agent's own config file; like every write, auth only accepts it
// from this page (see fromThisPage).
func (s *Server) agentChange(connect bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.Admin == nil {
			writeError(w, http.StatusNotImplemented, "settings unavailable")
			return
		}
		if err := s.Admin.AgentChange(r.Context(), r.PathValue("key"), connect); err != nil {
			agentError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, s.Admin.Settings(r.Context()))
	}
}

// agentError shows the reason as is: it's about the user's own config
// (not installed, a file Shiplino won't edit) and says how to fix it.
func agentError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrUnknownAgent) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeError(w, http.StatusConflict, err.Error())
}

func (s *Server) testNotification(w http.ResponseWriter, r *http.Request) {
	if s.Admin == nil {
		writeError(w, http.StatusNotImplemented, "settings unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := s.Admin.TestNotification(ctx); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
