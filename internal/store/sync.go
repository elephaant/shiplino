// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// SyncRow is a stored event as the sync client reads it.
type SyncRow struct {
	RowID     int64
	Body      []byte
	ProjectID string // the event's project, else its session's or root session's ("" if unknown)
}

// SyncRows returns committed events after rowid `after`, in the order
// they were stored. It reads through the read-only pool.
func (s *Store) SyncRows(ctx context.Context, after int64, limit int) ([]SyncRow, error) {
	rows, err := s.ro.QueryContext(ctx, `
		SELECT e.rowid, e.body,
		       coalesce(json_extract(e.body, '$.project.id'), ss.project_id, rs.project_id, '')
		FROM events e
		LEFT JOIN sessions ss ON ss.id = e.session_id
		LEFT JOIN sessions rs ON rs.id = ss.root_id
		WHERE e.rowid > ? ORDER BY e.rowid LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SyncRow
	for rows.Next() {
		var r SyncRow
		if err := rows.Scan(&r.RowID, &r.Body, &r.ProjectID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SyncPending counts the events stored after rowid `after`.
func (s *Store) SyncPending(ctx context.Context, after int64) (int64, error) {
	var n int64
	err := s.ro.QueryRowContext(ctx, `SELECT count(*) FROM events WHERE rowid > ?`, after).Scan(&n)
	return n, err
}

// SyncState is the sync progress for one workspace.
type SyncState struct {
	WorkspaceID string
	Cursor      int64  // last events rowid uploaded or skipped
	Scope       string // the allow/exclude lists the cursor was reached with
	Uploaded    int64  // new events the service accepted
	Rejected    int64  // events the service dropped as invalid
	LastUpload  time.Time
	LastError   string
	LastErrorAt time.Time
}

// SyncState returns a workspace's progress (zero if it has none yet).
func (s *Store) SyncState(ctx context.Context, workspace string) (SyncState, error) {
	st := SyncState{WorkspaceID: workspace}
	var up, errAt int64
	err := s.ro.QueryRowContext(ctx,
		`SELECT cursor, scope, uploaded, rejected, last_upload_at, last_error, last_error_at FROM sync_state WHERE workspace_id = ?`, workspace).
		Scan(&st.Cursor, &st.Scope, &st.Uploaded, &st.Rejected, &up, &st.LastError, &errAt)
	if errors.Is(err, sql.ErrNoRows) {
		return st, nil
	}
	st.LastUpload, st.LastErrorAt = millis(up), millis(errAt)
	return st, err
}

// PutSyncState saves a workspace's progress.
func (s *Store) PutSyncState(ctx context.Context, st SyncState) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sync_state (workspace_id, cursor, scope, uploaded, rejected, last_upload_at, last_error, last_error_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(workspace_id) DO UPDATE SET cursor = excluded.cursor, scope = excluded.scope,
		  uploaded = excluded.uploaded, rejected = excluded.rejected, last_upload_at = excluded.last_upload_at,
		  last_error = excluded.last_error, last_error_at = excluded.last_error_at`,
		st.WorkspaceID, st.Cursor, st.Scope, st.Uploaded, st.Rejected, unixMilli(st.LastUpload), st.LastError, unixMilli(st.LastErrorAt))
	return err
}

func millis(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

func unixMilli(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}
