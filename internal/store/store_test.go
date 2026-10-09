// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/elephaant/shiplino/pkg/engine"
	"github.com/elephaant/shiplino/pkg/model"
	"github.com/elephaant/shiplino/pkg/projects"
)

var ctx = context.Background()

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data", "shiplino.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func event(key string, ts time.Time) model.Event {
	return model.Event{
		ID: model.NewULID(ts), V: 1, TS: ts, Kind: model.KindToolStart, Agent: model.Agent{Name: "claude-code"},
		Collector: model.CollectorHook, SessionID: "claude-code:s1", ActorID: "claude-code:s1", DedupKey: key,
		Data: map[string]any{"tool": "edit"},
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	s, path := openTemp(t)
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	var v int
	if err := s2.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != len(migrations) {
		t.Fatalf("user_version = %d, %v", v, err)
	}
	var mode string
	if err := s2.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q, %v", mode, err)
	}
}

func TestInsertEventDedups(t *testing.T) {
	s, _ := openTemp(t)
	now := time.Now()
	tx, err := s.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if isNew, err := tx.InsertEvent(ctx, event("k1", now)); err != nil || !isNew {
		t.Fatalf("first insert: new=%v err=%v", isNew, err)
	}
	if isNew, err := tx.InsertEvent(ctx, event("k1", now.Add(time.Second))); err != nil || isNew {
		t.Fatalf("duplicate insert: new=%v err=%v", isNew, err)
	}
	if _, err := tx.InsertEvent(ctx, event("", now)); err == nil {
		t.Fatal("event without dedup key accepted")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	evs, err := s.Events(ctx, "claude-code:s1", "", 0)
	if err != nil || len(evs) != 1 || evs[0].Data["tool"] != "edit" {
		t.Fatalf("events = %v, %v", evs, err)
	}
}

func TestRollbackDiscardsEverything(t *testing.T) {
	s, _ := openTemp(t)
	tx, _ := s.Begin(ctx)
	tx.InsertEvent(ctx, event("k1", time.Now()))
	tx.PutCursor(ctx, Cursor{Source: "spool/a.jsonl", Offset: 100})
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if evs, _ := s.Events(ctx, "claude-code:s1", "", 0); len(evs) != 0 {
		t.Fatal("event survived rollback")
	}
	if cur, _ := s.Cursors(ctx); len(cur) != 0 {
		t.Fatal("cursor survived rollback")
	}
}

func TestSessionsAndCursorsPersist(t *testing.T) {
	s, path := openTemp(t)
	now := time.Now().Truncate(time.Millisecond)
	tx, _ := s.Begin(ctx)
	older := &engine.Session{ID: "claude-code:a", RootID: "claude-code:a", Agent: "claude-code", Status: engine.StatusDone, StartedAt: now, LastEventAt: now}
	newer := &engine.Session{ID: "claude-code:b", RootID: "claude-code:b", Agent: "claude-code", Status: engine.StatusRunning,
		StartedAt: now, LastEventAt: now.Add(time.Minute), Files: []string{"a.go"}, CostUSD: 0.42}
	for _, sess := range []*engine.Session{older, newer} {
		if err := tx.PutSession(ctx, sess); err != nil {
			t.Fatal(err)
		}
	}
	tx.PutCursor(ctx, Cursor{Source: "claude-code/b.jsonl", Offset: 512})
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	list, err := s2.Sessions(ctx, 0)
	if err != nil || len(list) != 2 || list[0].ID != "claude-code:b" {
		t.Fatalf("sessions = %v, %v", list, err)
	}
	if list[0].CostUSD != 0.42 || list[0].FilesChanged() != 1 {
		t.Fatalf("session body: %+v", list[0])
	}
	one, err := s2.Session(ctx, "claude-code:a")
	if err != nil || one == nil || one.Status != engine.StatusDone {
		t.Fatalf("Session() = %+v, %v", one, err)
	}
	if missing, err := s2.Session(ctx, "nope"); err != nil || missing != nil {
		t.Fatalf("missing session = %+v, %v", missing, err)
	}
	cur, _ := s2.Cursors(ctx)
	if cur["claude-code/b.jsonl"] != 512 {
		t.Fatalf("cursors = %v", cur)
	}
}

func TestUpgradeFromV1KeepsSessions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(migrations[0]); err != nil {
		t.Fatal(err)
	}
	db.Exec(`PRAGMA user_version = 1`)
	db.Exec(`INSERT INTO sessions (id, root_id, agent, status, started_at, last_event_at, body)
		VALUES ('claude-code:old', 'claude-code:old', 'claude-code', 'done', 1, 1, '{"id":"claude-code:old","status":"done"}')`)
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("upgrade failed: %v", err)
	}
	defer s.Close()
	got, err := s.Session(ctx, "claude-code:old")
	if err != nil || got == nil || got.Status != engine.StatusDone {
		t.Fatalf("session lost in upgrade: %+v %v", got, err)
	}
	if list, err := s.Projects(ctx); err != nil || len(list) != 0 {
		t.Fatalf("projects after upgrade: %v %v", list, err)
	}
}

func TestProjectsSummary(t *testing.T) {
	s, _ := openTemp(t)
	now := time.Now().Truncate(time.Millisecond)
	tx, _ := s.Begin(ctx)
	p := projects.Project{ID: "github.com/acme/api", Name: "api", Kind: "remote", Remote: "github.com/acme/api"}
	tx.PutProject(ctx, p, now.Add(-time.Hour))
	tx.PutProject(ctx, p, now) // last_seen moves forward, first_seen stays
	for i, st := range []engine.Status{engine.StatusRunning, engine.StatusRunning, engine.StatusDone} {
		tx.PutSession(ctx, &engine.Session{ID: fmt.Sprintf("claude-code:%d", i), RootID: fmt.Sprintf("claude-code:%d", i), Agent: "claude-code",
			Status: st, ProjectID: p.ID, StartedAt: now, LastEventAt: now, BestCostUSD: 0.5})
	}
	tx.PutSession(ctx, &engine.Session{ID: "claude-code:0/sub:x", RootID: "claude-code:0", ParentID: "claude-code:0", Agent: "claude-code",
		Status: engine.StatusRunning, ProjectID: p.ID, StartedAt: now, LastEventAt: now})
	tx.Commit()

	list, err := s.Projects(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("%v %v", list, err)
	}
	got := list[0]
	if got.Counts["running"] != 2 || got.Counts["done"] != 1 || got.Sessions != 3 || got.CostUSD != 1.5 {
		t.Fatalf("summary (subagents must not count): %+v", got)
	}
	if !got.FirstSeen.Equal(now.Add(-time.Hour)) || !got.LastSeen.Equal(now) {
		t.Fatalf("seen: %v %v", got.FirstSeen, got.LastSeen)
	}
	in, _ := s.SessionsIn(ctx, p.ID, 0)
	other, _ := s.SessionsIn(ctx, "github.com/acme/web", 0)
	if len(in) != 4 || len(other) != 0 {
		t.Fatalf("filter: %d %d", len(in), len(other))
	}
}
