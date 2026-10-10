// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package store

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/elephaant/shiplino/pkg/engine"
	"github.com/elephaant/shiplino/pkg/model"
)

// TimelineSessions returns the top-level sessions of a project ("" for
// all) that were active in [from, to), plus all their subagents. Sessions
// still running or waiting count as active up to now. At most limit
// top-level sessions are returned, the most recently active first.
func (s *Store) TimelineSessions(ctx context.Context, projectID string, from, to time.Time, limit int) ([]*engine.Session, error) {
	roots, err := s.sessionBodies(ctx,
		`SELECT body FROM sessions
		 WHERE parent_id IS NULL AND (? = '' OR project_id = ?) AND started_at < ?
		   AND (last_event_at >= ? OR status IN ('running', 'waiting'))
		 ORDER BY last_event_at DESC LIMIT ?`,
		projectID, projectID, to.UnixMilli(), from.UnixMilli(), limit)
	if err != nil || len(roots) == 0 {
		return roots, err
	}
	ids := make([]any, len(roots))
	for i, r := range roots {
		ids[i] = r.ID
	}
	subs, err := s.sessionBodies(ctx,
		`SELECT body FROM sessions WHERE parent_id IS NOT NULL AND root_id IN (`+placeholders(len(ids))+`)`, ids...)
	return append(roots, subs...), err
}

// TimelineMarks returns, in time order, the lifecycle events (see
// engine.MarkKinds) of the given top-level sessions before `to`. Only
// indexed columns are read, plus the subagent id for subagent.end.
func (s *Store) TimelineMarks(ctx context.Context, rootIDs []string, to time.Time) (map[string][]engine.Mark, error) {
	out := map[string][]engine.Mark{}
	if len(rootIDs) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(rootIDs)+len(engine.MarkKinds)+1)
	for _, id := range rootIDs {
		args = append(args, id)
	}
	for _, k := range engine.MarkKinds {
		args = append(args, string(k))
	}
	args = append(args, to.UnixMilli())
	rows, err := s.db.QueryContext(ctx,
		`SELECT session_id, ts, kind, coalesce(nullif(actor_id, ''), session_id),
		        CASE kind WHEN 'subagent.end' THEN coalesce(json_extract(body, '$.data.child_session_id'), '') ELSE '' END
		 FROM events
		 WHERE session_id IN (`+placeholders(len(rootIDs))+`) AND kind IN (`+placeholders(len(engine.MarkKinds))+`) AND ts < ?
		 ORDER BY ts, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var sid, kind string
		var ts int64
		var m engine.Mark
		if err := rows.Scan(&sid, &ts, &kind, &m.Actor, &m.Child); err != nil {
			return nil, err
		}
		m.TS, m.Kind = time.UnixMilli(ts).UTC(), model.Kind(kind)
		out[sid] = append(out[sid], m)
	}
	return out, rows.Err()
}

// FileEdit is a file.edit event without its patch text.
type FileEdit struct {
	EventID      string
	TS           time.Time
	Actor        string
	Path, Op     string
	LinesAdded   int
	LinesRemoved int
	HasPatch     bool
	Omitted      string // why no patch was kept, e.g. "secret_file"
}

// FileEdits lists a session's file edits in time order, without reading
// patch text out of the database. actorID ("" for every actor) limits it
// to one session or subagent.
func (s *Store) FileEdits(ctx context.Context, rootID, actorID string) ([]FileEdit, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, ts, coalesce(nullif(actor_id, ''), session_id),
		        coalesce(json_extract(body, '$.data.path'), ''), coalesce(json_extract(body, '$.data.op'), ''),
		        coalesce(json_extract(body, '$.data.lines_added'), 0), coalesce(json_extract(body, '$.data.lines_removed'), 0),
		        json_type(body, '$.data.patch') IS NOT NULL, coalesce(json_extract(body, '$.data.patch_omitted'), '')
		 FROM events
		 WHERE session_id = ? AND kind = 'file.edit' AND (? = '' OR coalesce(nullif(actor_id, ''), session_id) = ?)
		 ORDER BY ts, id`, rootID, actorID, actorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FileEdit
	for rows.Next() {
		var f FileEdit
		var ts int64
		if err := rows.Scan(&f.EventID, &ts, &f.Actor, &f.Path, &f.Op, &f.LinesAdded, &f.LinesRemoved, &f.HasPatch, &f.Omitted); err != nil {
			return nil, err
		}
		f.TS = time.UnixMilli(ts).UTC()
		out = append(out, f)
	}
	return out, rows.Err()
}

// FileEditEvents returns a session's file.edit events for one path, with
// their patches, in time order.
func (s *Store) FileEditEvents(ctx context.Context, rootID, actorID, path string, limit int) ([]model.Event, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT body FROM events
		 WHERE session_id = ? AND kind = 'file.edit' AND json_extract(body, '$.data.path') = ?
		   AND (? = '' OR coalesce(nullif(actor_id, ''), session_id) = ?)
		 ORDER BY ts, id LIMIT ?`, rootID, path, actorID, actorID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Event
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var e model.Event
		if err := json.Unmarshal(body, &e); err != nil {
			continue // unreadable rows are skipped, as at ingest
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) sessionBodies(ctx context.Context, q string, args ...any) ([]*engine.Session, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*engine.Session
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var sess engine.Session
		if err := json.Unmarshal(body, &sess); err != nil {
			return nil, err
		}
		out = append(out, &sess)
	}
	return out, rows.Err()
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
