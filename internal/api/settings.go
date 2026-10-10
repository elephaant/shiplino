package api

import (
	"context"
	"encoding/json"
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
}

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
