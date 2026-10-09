// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/elephaant/shiplino/pkg/engine"
	"github.com/elephaant/shiplino/pkg/model"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registered as "sqlite"
)

// Store is the local SQLite database.
type Store struct {
	db *sql.DB
}

// Open opens (and creates or migrates) the database at path.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.ToSlash(path) +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite allows one writer. A single connection keeps writes serialized
	// without lock errors; reads are fast enough for the local daemon.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// migrations are applied in order; PRAGMA user_version records progress.
var migrations = []string{
	// v1
	`CREATE TABLE events (
		id TEXT PRIMARY KEY,
		ts INTEGER NOT NULL,
		kind TEXT NOT NULL,
		agent TEXT NOT NULL,
		session_id TEXT NOT NULL,
		actor_id TEXT,
		turn_id TEXT,
		collector TEXT NOT NULL,
		dedup_key TEXT NOT NULL UNIQUE,
		body TEXT NOT NULL
	);
	CREATE INDEX events_session_ts ON events(session_id, ts);
	CREATE INDEX events_kind_ts ON events(kind, ts);

	CREATE TABLE sessions (
		id TEXT PRIMARY KEY,
		root_id TEXT NOT NULL,
		parent_id TEXT,
		agent TEXT NOT NULL,
		status TEXT NOT NULL,
		started_at INTEGER NOT NULL,
		last_event_at INTEGER NOT NULL,
		body TEXT NOT NULL
	);
	CREATE INDEX sessions_last_event ON sessions(last_event_at);
	CREATE INDEX sessions_root ON sessions(root_id);

	CREATE TABLE cursors (
		source TEXT PRIMARY KEY,
		offset INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	);`,
}

func (s *Store) migrate(ctx context.Context) error {
	var v int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	for i := v; i < len(migrations); i++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("v%d: %w", i+1, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Cursor is how far a source (e.g. one spool file) has been processed.
type Cursor struct {
	Source string
	Offset int64
}

// Tx is one write transaction. Events, the session snapshots they
// produced and the cursors that cover them commit together, so a crash
// never loses or double-counts an event.
type Tx struct {
	tx *sql.Tx
}

// Begin starts a write transaction.
func (s *Store) Begin(ctx context.Context) (*Tx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &Tx{tx: tx}, nil
}

// InsertEvent stores e unless an event with the same dedup key exists.
// It reports whether the event was new; only new events may be applied
// to the engine.
func (t *Tx) InsertEvent(ctx context.Context, e model.Event) (bool, error) {
	if e.DedupKey == "" {
		return false, errors.New("store: event without dedup key")
	}
	body, err := json.Marshal(e)
	if err != nil {
		return false, err
	}
	res, err := t.tx.ExecContext(ctx,
		`INSERT INTO events (id, ts, kind, agent, session_id, actor_id, turn_id, collector, dedup_key, body)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(dedup_key) DO NOTHING`,
		e.ID, e.TS.UnixMilli(), string(e.Kind), e.Agent.Name, e.SessionID, e.ActorID, e.TurnID,
		string(e.Collector), e.DedupKey, body)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// PutSession writes a session snapshot.
func (t *Tx) PutSession(ctx context.Context, s *engine.Session) error {
	body, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = t.tx.ExecContext(ctx,
		`INSERT INTO sessions (id, root_id, parent_id, agent, status, started_at, last_event_at, body)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   root_id = excluded.root_id, parent_id = excluded.parent_id, agent = excluded.agent,
		   status = excluded.status, started_at = excluded.started_at,
		   last_event_at = excluded.last_event_at, body = excluded.body`,
		s.ID, s.RootID, nullable(s.ParentID), s.Agent, string(s.Status),
		s.StartedAt.UnixMilli(), s.LastEventAt.UnixMilli(), body)
	return err
}

// PutCursor records progress for a source.
func (t *Tx) PutCursor(ctx context.Context, c Cursor) error {
	_, err := t.tx.ExecContext(ctx,
		`INSERT INTO cursors (source, offset, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(source) DO UPDATE SET offset = excluded.offset, updated_at = excluded.updated_at`,
		c.Source, c.Offset, time.Now().UnixMilli())
	return err
}

// DeleteCursor forgets a source, e.g. after its spool file was removed.
func (t *Tx) DeleteCursor(ctx context.Context, source string) error {
	_, err := t.tx.ExecContext(ctx, `DELETE FROM cursors WHERE source = ?`, source)
	return err
}

// Commit commits the transaction.
func (t *Tx) Commit() error { return t.tx.Commit() }

// Rollback aborts the transaction. It is safe to call after Commit.
func (t *Tx) Rollback() error {
	err := t.tx.Rollback()
	if errors.Is(err, sql.ErrTxDone) {
		return nil
	}
	return err
}

// Cursors returns every stored cursor keyed by source.
func (s *Store) Cursors(ctx context.Context) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT source, offset FROM cursors`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var src string
		var off int64
		if err := rows.Scan(&src, &off); err != nil {
			return nil, err
		}
		out[src] = off
	}
	return out, rows.Err()
}

// Sessions returns sessions, most recently active first. limit <= 0 means all.
func (s *Store) Sessions(ctx context.Context, limit int) ([]*engine.Session, error) {
	q := `SELECT body FROM sessions ORDER BY last_event_at DESC`
	args := []any{}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
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

// Session returns one session, or nil if it doesn't exist.
func (s *Store) Session(ctx context.Context, id string) (*engine.Session, error) {
	var body []byte
	err := s.db.QueryRowContext(ctx, `SELECT body FROM sessions WHERE id = ?`, id).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var sess engine.Session
	return &sess, json.Unmarshal(body, &sess)
}

// Events returns a session's events (including its subagents') in time
// order, after the event id `after` (exclusive, "" for the start).
func (s *Store) Events(ctx context.Context, sessionID, after string, limit int) ([]model.Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT body FROM events WHERE session_id = ? AND id > ? ORDER BY ts, id LIMIT ?`,
		sessionID, after, limit)
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
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
