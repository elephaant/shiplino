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
	"strings"
	"time"

	"github.com/elephaant/shiplino/pkg/board"
	"github.com/elephaant/shiplino/pkg/engine"
	"github.com/elephaant/shiplino/pkg/model"
	"github.com/elephaant/shiplino/pkg/projects"

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
	// v2: projects
	`CREATE TABLE projects (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		kind TEXT NOT NULL,
		remote TEXT,
		repo_root TEXT,
		first_seen INTEGER NOT NULL,
		last_seen INTEGER NOT NULL
	);
	ALTER TABLE sessions ADD COLUMN project_id TEXT;
	CREATE INDEX sessions_project ON sessions(project_id, last_event_at);`,
	// v3: user changes to cards (pins, positions, notes) and manual cards
	`CREATE TABLE cards (
		id TEXT PRIMARY KEY,
		project_id TEXT NOT NULL,
		origin TEXT NOT NULL,
		title TEXT,
		notes TEXT,
		col TEXT,
		position REAL,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	);
	CREATE INDEX cards_project ON cards(project_id);`,
	// v4: full-text search over prompts, commands, file paths, titles and
	// commit messages (see searchText), backfilled from stored events.
	`CREATE VIRTUAL TABLE search USING fts5(
		text, event_id UNINDEXED, session_id UNINDEXED, kind UNINDEXED, ts UNINDEXED,
		tokenize = 'unicode61 remove_diacritics 2', prefix = '2 3'
	);
	INSERT INTO search (text, event_id, session_id, kind, ts)
	SELECT t, id, session_id, kind, ts FROM (
		SELECT id, session_id, kind, ts, CASE kind
			WHEN 'turn.start' THEN json_extract(body, '$.data.prompt')
			WHEN 'shell.exec' THEN json_extract(body, '$.data.command')
			WHEN 'file.edit' THEN json_extract(body, '$.data.path')
			WHEN 'session.start' THEN json_extract(body, '$.data.title')
			WHEN 'session.update' THEN json_extract(body, '$.data.title')
			WHEN 'git.commit' THEN json_extract(body, '$.data.message')
			WHEN 'git.pr' THEN json_extract(body, '$.data.url')
		END AS t FROM events
	) WHERE t IS NOT NULL AND t != '';`,
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
	if err != nil || n != 1 {
		return false, err
	}
	if text := searchText(e); text != "" {
		if _, err := t.tx.ExecContext(ctx, `INSERT INTO search (text, event_id, session_id, kind, ts) VALUES (?, ?, ?, ?, ?)`,
			text, e.ID, e.SessionID, string(e.Kind), e.TS.UnixMilli()); err != nil {
			return false, err
		}
	}
	return true, nil
}

// searchText is the searchable text of an event: prompts, commands, file
// paths, titles and commit messages. Keep in sync with migration v4.
func searchText(e model.Event) string {
	key := map[model.Kind]string{
		model.KindTurnStart: "prompt", model.KindShellExec: "command", model.KindFileEdit: "path",
		model.KindSessionStart: "title", model.KindSessionUpdate: "title", model.KindGitCommit: "message", model.KindGitPR: "url",
	}[e.Kind]
	v, _ := e.Data[key].(string)
	return v
}

// Hit is one search result.
type Hit struct {
	EventID   string    `json:"event_id"`
	SessionID string    `json:"session_id"`
	Kind      string    `json:"kind"`
	TS        time.Time `json:"ts"`
	Snippet   string    `json:"snippet"` // matches wrapped in « »
	Title     string    `json:"session_title,omitempty"`
	Agent     string    `json:"agent,omitempty"`
	ProjectID string    `json:"project_id,omitempty"`
}

// Search finds events matching query, newest first. Every word must
// match, as a prefix; projectID ("" for all) limits it to one project.
func (s *Store) Search(ctx context.Context, query, projectID string, limit int) ([]Hit, error) {
	q := ftsQuery(query)
	if q == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT f.event_id, f.session_id, f.kind, f.ts, snippet(search, 0, '«', '»', '…', 16),
		        coalesce(json_extract(ss.body, '$.title'), ''), coalesce(ss.agent, ''), coalesce(ss.project_id, '')
		 FROM search f LEFT JOIN sessions ss ON ss.id = f.session_id
		 WHERE search MATCH ? AND (? = '' OR ss.project_id = ?)
		 ORDER BY f.ts DESC LIMIT ?`, q, projectID, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var h Hit
		var ts int64
		if err := rows.Scan(&h.EventID, &h.SessionID, &h.Kind, &ts, &h.Snippet, &h.Title, &h.Agent, &h.ProjectID); err != nil {
			return nil, err
		}
		h.TS = time.UnixMilli(ts).UTC()
		out = append(out, h)
	}
	return out, rows.Err()
}

// ftsQuery turns user input into a safe FTS5 query: each word becomes a
// quoted prefix term, so punctuation and FTS operators are literal.
func ftsQuery(in string) string {
	var terms []string
	for _, w := range strings.Fields(in) {
		w = strings.ReplaceAll(w, `"`, "")
		if w != "" {
			terms = append(terms, `"`+w+`"*`)
		}
	}
	return strings.Join(terms, " ")
}

// PutSession writes a session snapshot.
func (t *Tx) PutSession(ctx context.Context, s *engine.Session) error {
	body, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = t.tx.ExecContext(ctx,
		`INSERT INTO sessions (id, root_id, parent_id, agent, status, started_at, last_event_at, body, project_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   root_id = excluded.root_id, parent_id = excluded.parent_id, agent = excluded.agent,
		   status = excluded.status, started_at = excluded.started_at,
		   last_event_at = excluded.last_event_at, body = excluded.body, project_id = excluded.project_id`,
		s.ID, s.RootID, nullable(s.ParentID), s.Agent, string(s.Status),
		s.StartedAt.UnixMilli(), s.LastEventAt.UnixMilli(), body, nullable(s.ProjectID))
	return err
}

// PutProject inserts or refreshes a project; first_seen never moves.
func (t *Tx) PutProject(ctx context.Context, p projects.Project, seen time.Time) error {
	_, err := t.tx.ExecContext(ctx,
		`INSERT INTO projects (id, name, kind, remote, repo_root, first_seen, last_seen)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   name = excluded.name, kind = excluded.kind, remote = excluded.remote, repo_root = excluded.repo_root,
		   last_seen = MAX(projects.last_seen, excluded.last_seen)`,
		p.ID, p.Name, p.Kind, nullable(p.Remote), nullable(p.RepoRoot), seen.UnixMilli(), seen.UnixMilli())
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

// Overrides returns the user's card changes for a project, by card id.
func (s *Store) Overrides(ctx context.Context, projectID string) (map[string]board.Override, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, project_id, origin, COALESCE(title,''), COALESCE(notes,''), COALESCE(col,''), COALESCE(position,0), created_at FROM cards WHERE project_id = ?`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]board.Override{}
	for rows.Next() {
		var o board.Override
		var created int64
		if err := rows.Scan(&o.CardID, &o.ProjectID, &o.Origin, &o.Title, &o.Notes, &o.Column, &o.Position, &created); err != nil {
			return nil, err
		}
		o.CreatedAt = time.UnixMilli(created)
		out[o.CardID] = o
	}
	return out, rows.Err()
}

// Override returns one card's override, or nil.
func (s *Store) Override(ctx context.Context, cardID string) (*board.Override, error) {
	var o board.Override
	var created int64
	err := s.db.QueryRowContext(ctx, `SELECT id, project_id, origin, COALESCE(title,''), COALESCE(notes,''), COALESCE(col,''), COALESCE(position,0), created_at FROM cards WHERE id = ?`, cardID).
		Scan(&o.CardID, &o.ProjectID, &o.Origin, &o.Title, &o.Notes, &o.Column, &o.Position, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	o.CreatedAt = time.UnixMilli(created)
	return &o, err
}

// PutOverride saves a card's override.
func (s *Store) PutOverride(ctx context.Context, o board.Override) error {
	now := time.Now().UnixMilli()
	if o.CreatedAt.IsZero() {
		o.CreatedAt = time.Now()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO cards (id, project_id, origin, title, notes, col, position, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET title = excluded.title, notes = excluded.notes, col = excluded.col,
		   position = excluded.position, updated_at = excluded.updated_at`,
		o.CardID, o.ProjectID, o.Origin, nullable(o.Title), nullable(o.Notes), nullable(o.Column), o.Position, o.CreatedAt.UnixMilli(), now)
	return err
}

// DeleteOverride removes a card override (and so a manual card).
func (s *Store) DeleteOverride(ctx context.Context, cardID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM cards WHERE id = ?`, cardID)
	return err
}

// Project returns one project summary, or nil.
func (s *Store) Project(ctx context.Context, id string) (*ProjectSummary, error) {
	list, err := s.Projects(ctx)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].ID == id {
			return &list[i], nil
		}
	}
	return nil, nil
}

// ProjectSummary is a project with live counts of its root sessions.
type ProjectSummary struct {
	projects.Project
	FirstSeen time.Time      `json:"first_seen"`
	LastSeen  time.Time      `json:"last_seen"`
	Counts    map[string]int `json:"counts"` // by status
	Sessions  int            `json:"sessions"`
	CostUSD   float64        `json:"cost_usd"`
}

// Projects returns every project with session counts, most recent first.
func (s *Store) Projects(ctx context.Context) ([]ProjectSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, kind, COALESCE(remote,''), COALESCE(repo_root,''), first_seen, last_seen FROM projects ORDER BY last_seen DESC`)
	if err != nil {
		return nil, err
	}
	var out []ProjectSummary
	index := map[string]int{}
	for rows.Next() {
		var p ProjectSummary
		var first, last int64
		if err := rows.Scan(&p.ID, &p.Name, &p.Kind, &p.Remote, &p.RepoRoot, &first, &last); err != nil {
			rows.Close()
			return nil, err
		}
		p.FirstSeen, p.LastSeen, p.Counts = time.UnixMilli(first), time.UnixMilli(last), map[string]int{}
		index[p.ID] = len(out)
		out = append(out, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	agg, err := s.db.QueryContext(ctx, `
		SELECT project_id, status, COUNT(*), COALESCE(SUM(json_extract(body, '$.best_cost_usd')), 0)
		FROM sessions WHERE parent_id IS NULL AND project_id IS NOT NULL GROUP BY 1, 2`)
	if err != nil {
		return nil, err
	}
	defer agg.Close()
	for agg.Next() {
		var id, status string
		var n int
		var cost float64
		if err := agg.Scan(&id, &status, &n, &cost); err != nil {
			return nil, err
		}
		if i, ok := index[id]; ok {
			out[i].Counts[status] += n
			out[i].Sessions += n
			out[i].CostUSD += cost
		}
	}
	return out, agg.Err()
}

// Sessions returns sessions, most recently active first. limit <= 0 means all.
func (s *Store) Sessions(ctx context.Context, limit int) ([]*engine.Session, error) {
	return s.SessionsIn(ctx, "", limit)
}

// SessionsIn is Sessions limited to one project ("" = all projects).
func (s *Store) SessionsIn(ctx context.Context, projectID string, limit int) ([]*engine.Session, error) {
	q := `SELECT body FROM sessions`
	args := []any{}
	if projectID != "" {
		q += ` WHERE project_id = ?`
		args = append(args, projectID)
	}
	q += ` ORDER BY last_event_at DESC`
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

// ProcessTotals returns, per agent process, the highest running cost total
// the agent has reported (cost-report usage events), so the engine can keep
// splitting process totals between sessions after a restart.
func (s *Store) ProcessTotals(ctx context.Context) (map[string]float64, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT json_extract(body, '$.data.process'), MAX(json_extract(body, '$.data.total_cost_usd'))
		FROM events
		WHERE kind = 'usage' AND json_extract(body, '$.data.report') = 1
		GROUP BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var proc sql.NullString
		var total sql.NullFloat64
		if err := rows.Scan(&proc, &total); err != nil {
			return nil, err
		}
		if proc.Valid && total.Valid {
			out[proc.String] = total.Float64
		}
	}
	return out, rows.Err()
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
