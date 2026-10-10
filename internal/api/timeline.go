// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

package api

import (
	"net/http"
	"slices"
	"sort"
	"time"

	"github.com/elephaant/shiplino/internal/store"
	"github.com/elephaant/shiplino/pkg/engine"
	"github.com/elephaant/shiplino/pkg/model"
)

// TimelineRow is one session or subagent on the timeline.
type TimelineRow struct {
	ID        string           `json:"id"`
	ParentID  string           `json:"parent_id,omitempty"`
	RootID    string           `json:"root_id"`
	Depth     int              `json:"depth"`
	Agent     string           `json:"agent"`
	ActorType string           `json:"actor_type,omitempty"`
	Title     string           `json:"title,omitempty"`
	Status    engine.Status    `json:"status"`
	ProjectID string           `json:"project_id,omitempty"`
	StartedAt time.Time        `json:"started_at"`
	EndedAt   time.Time        `json:"ended_at,omitzero"`
	CostUSD   float64          `json:"cost_usd"`
	Segments  []engine.Segment `json:"segments"`
}

// Timeline is the response of GET /api/v1/timeline.
type Timeline struct {
	From time.Time     `json:"from"`
	To   time.Time     `json:"to"`
	Now  time.Time     `json:"now"`
	Rows []TimelineRow `json:"rows"`
}

const (
	maxTimelineSpan  = 31 * 24 * time.Hour
	maxTimelineRoots = 200
)

// timeline: GET /api/v1/timeline?project=&from=&to= (RFC 3339; the
// default is the last 24 hours). Segments are computed from stored
// lifecycle events (see engine.Segments).
func (s *Server) timeline(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	now := time.Now().UTC()
	to, from := now, now.Add(-24*time.Hour)
	for _, p := range []struct {
		key string
		dst *time.Time
	}{{"from", &from}, {"to", &to}} {
		if v := q.Get(p.key); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				writeError(w, http.StatusBadRequest, p.key+" must be an RFC 3339 time")
				return
			}
			*p.dst = t.UTC()
		}
	}
	if !to.After(from) || to.Sub(from) > maxTimelineSpan {
		writeError(w, http.StatusBadRequest, "from must be before to, at most 31 days apart")
		return
	}
	sessions, err := s.st.TimelineSessions(r.Context(), q.Get("project"), from, to, maxTimelineRoots)
	if err != nil {
		s.internal(w, err)
		return
	}
	var roots []string
	for _, ss := range sessions {
		if ss.ParentID == "" {
			roots = append(roots, ss.ID)
		}
	}
	marks, err := s.st.TimelineMarks(r.Context(), roots, to)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, Timeline{From: from, To: to, Now: now, Rows: buildTimeline(sessions, marks, from, to, now)})
}

// buildTimeline orders sessions oldest first, each followed by its
// subagents (depth first), and drops rows with nothing in the window.
func buildTimeline(sessions []*engine.Session, marks map[string][]engine.Mark, from, to, now time.Time) []TimelineRow {
	children := map[string][]*engine.Session{}
	var roots []*engine.Session
	for _, ss := range sessions {
		if ss.ParentID == "" {
			roots = append(roots, ss)
		} else {
			children[ss.ParentID] = append(children[ss.ParentID], ss)
		}
	}
	byStart := func(l []*engine.Session) {
		sort.Slice(l, func(i, j int) bool {
			if !l[i].StartedAt.Equal(l[j].StartedAt) {
				return l[i].StartedAt.Before(l[j].StartedAt)
			}
			return l[i].ID < l[j].ID
		})
	}
	byStart(roots)
	rows := []TimelineRow{}
	var add func(ss *engine.Session)
	add = func(ss *engine.Session) {
		segs := engine.Clip(engine.Segments(ss, marks[ss.RootID], now), from, to)
		if len(segs) > 0 {
			cost := ss.CostUSD
			if ss.ParentID == "" {
				cost = ss.BestCostUSD
			}
			rows = append(rows, TimelineRow{
				ID: ss.ID, ParentID: ss.ParentID, RootID: ss.RootID, Depth: ss.Depth, Agent: ss.Agent, ActorType: ss.ActorType,
				Title: ss.Title, Status: ss.Status, ProjectID: ss.ProjectID, StartedAt: ss.StartedAt, EndedAt: ss.EndedAt,
				CostUSD: cost, Segments: segs,
			})
		}
		kids := children[ss.ID]
		byStart(kids)
		for _, c := range kids {
			add(c)
		}
	}
	for _, r := range roots {
		add(r)
	}
	return rows
}

// FileSummary is one file a session changed.
type FileSummary struct {
	Path         string    `json:"path"`
	Op           string    `json:"op"` // create if the session created it, else its last op
	Edits        int       `json:"edits"`
	LinesAdded   int       `json:"lines_added"`
	LinesRemoved int       `json:"lines_removed"`
	Patches      int       `json:"patches"` // edits with a stored diff
	Omitted      string    `json:"omitted,omitempty"`
	Actors       []string  `json:"actors"`
	LastAt       time.Time `json:"last_at"`
}

// sessionFiles: GET /api/v1/sessions/{id}/files lists changed files with
// per-file counts; ?path= returns that file's edits with their diffs.
func (s *Server) sessionFiles(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	root, actor := rootOf(id), ""
	if root != id {
		actor = id // a subagent: only its own edits
	}
	if path := r.URL.Query().Get("path"); path != "" {
		evs, err := s.st.FileEditEvents(r.Context(), root, actor, path, 500)
		if err != nil {
			s.internal(w, err)
			return
		}
		if evs == nil {
			evs = []model.Event{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"path": path, "edits": evs})
		return
	}
	edits, err := s.st.FileEdits(r.Context(), root, actor)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": summarizeFiles(edits)})
}

func summarizeFiles(edits []store.FileEdit) []FileSummary {
	out := []FileSummary{}
	index := map[string]int{}
	for _, e := range edits {
		if e.Path == "" {
			continue
		}
		i, ok := index[e.Path]
		if !ok {
			i = len(out)
			index[e.Path] = i
			out = append(out, FileSummary{Path: e.Path, Op: e.Op, Actors: []string{}})
		}
		f := &out[i]
		f.Edits++
		f.LinesAdded += e.LinesAdded
		f.LinesRemoved += e.LinesRemoved
		if e.HasPatch {
			f.Patches++
		}
		if e.Omitted != "" {
			f.Omitted = e.Omitted
		}
		if f.Op != "create" || e.Op == "delete" {
			f.Op = e.Op
		}
		if !slices.Contains(f.Actors, e.Actor) {
			f.Actors = append(f.Actors, e.Actor)
		}
		f.LastAt = e.TS
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
