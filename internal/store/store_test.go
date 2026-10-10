// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

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
		VALUES ('claude-code:old', 'claude-code:old', 'claude-code', 'done', 1, 1, '{"id":"claude-code:old","status":"done","best_cost_usd":0.25}')`)
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
	var cost float64 // v6 copies the cost out of the body
	if err := s.db.QueryRow(`SELECT cost_usd FROM sessions WHERE id = 'claude-code:old'`).Scan(&cost); err != nil || cost != 0.25 {
		t.Fatalf("cost_usd after upgrade = %v, %v", cost, err)
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

func searchEvent(key string, kind model.Kind, ts time.Time, data map[string]any) model.Event {
	e := event(key, ts)
	e.Kind, e.Data = kind, data
	return e
}

func TestSearch(t *testing.T) {
	s, _ := openTemp(t)
	at := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	tx, _ := s.Begin(ctx)
	tx.InsertEvent(ctx, searchEvent("a", model.KindTurnStart, at, map[string]any{"prompt": "fix the cart_total rounding bug"}))
	tx.InsertEvent(ctx, searchEvent("b", model.KindShellExec, at.Add(time.Second), map[string]any{"command": "npm test -- cart.spec.ts"}))
	tx.InsertEvent(ctx, searchEvent("c", model.KindFileEdit, at.Add(2*time.Second), map[string]any{"path": "/home/dev/shop/src/cart.ts"}))
	tx.InsertEvent(ctx, searchEvent("d", model.KindToolStart, at.Add(3*time.Second), map[string]any{"input_summary": "cart"}))
	tx.InsertEvent(ctx, searchEvent("a", model.KindTurnStart, at, map[string]any{"prompt": "fix the cart_total rounding bug"})) // duplicate
	tx.PutSession(ctx, &engine.Session{ID: "claude-code:s1", RootID: "claude-code:s1", Agent: "claude-code", Title: "Cart rounding", ProjectID: "p1", StartedAt: at, LastEventAt: at})
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	hits, err := s.Search(ctx, "cart", "", 0)
	if err != nil || len(hits) != 3 {
		t.Fatalf("hits=%+v err=%v", hits, err)
	}
	if hits[0].Kind != "file.edit" || hits[0].Title != "Cart rounding" || hits[0].ProjectID != "p1" {
		t.Fatalf("newest first, with session info: %+v", hits[0])
	}
	cases := map[string]int{
		"rounding":        1, // prompt
		"roun":            1, // prefix
		"cart.ts":         1, // path, punctuation kept literal
		"cart_total":      1,
		"npm cart":        1, // all words must match
		"nothing-matches": 0,
		"   ":             0,
	}
	for q, want := range cases {
		if got, err := s.Search(ctx, q, "", 0); err != nil || len(got) != want {
			t.Errorf("Search(%q) = %d hits (%v), want %d", q, len(got), err, want)
		}
	}
	if got, _ := s.Search(ctx, `cart" OR "x`, "", 0); len(got) != 0 {
		t.Errorf("FTS syntax wasn't neutralized: %d hits", len(got))
	}
	if got, _ := s.Search(ctx, "cart", "other-project", 0); len(got) != 0 {
		t.Errorf("project filter: %d hits", len(got))
	}
	if got, _ := s.Search(ctx, "rounding", "", 0); len(got) == 1 && got[0].Snippet != "fix the cart_total «rounding» bug" {
		t.Errorf("snippet: %q", got[0].Snippet)
	}
}

func TestSearchBackfillOnUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations[:3] {
		if _, err := db.Exec(m); err != nil {
			t.Fatal(err)
		}
	}
	db.Exec(`PRAGMA user_version = 3`)
	db.Exec(`INSERT INTO events (id, ts, kind, agent, session_id, collector, dedup_key, body)
		VALUES ('e1', 1, 'shell.exec', 'claude-code', 'claude-code:s1', 'hook', 'k1', '{"data":{"command":"go test ./..."}}')`)
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if hits, err := s.Search(ctx, "go test", "", 0); err != nil || len(hits) != 1 || hits[0].EventID != "e1" {
		t.Fatalf("backfill: %+v %v", hits, err)
	}
}

// Checkpoints run beside the writer; once the WAL is long, a commit
// finishes one so the WAL starts over instead of growing.
func TestLongWALStartsOver(t *testing.T) {
	old := walLongFrames
	walLongFrames = 10
	t.Cleanup(func() { walLongFrames = old })
	s, _ := openTemp(t)
	now := time.Now()
	n := 0
	write := func(events int) {
		t.Helper()
		tx, _ := s.Begin(ctx)
		for range events {
			n++
			if _, err := tx.InsertEvent(ctx, event(fmt.Sprintf("k%d", n), now)); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	walFrames := func() int {
		var busy, frames, done int
		if err := s.ckpt.QueryRow(`PRAGMA wal_checkpoint(PASSIVE)`).Scan(&busy, &frames, &done); err != nil {
			t.Fatal(err)
		}
		return frames
	}
	write(300)
	s.checkpoint()
	if !s.walLong.Load() {
		t.Fatalf("WAL of %d frames not seen as long", walFrames())
	}
	// Under steady load the writer appends while the checkpointer copies,
	// so pages are left over: the writer must finish the job.
	write(300)
	s.walLong.Store(true)
	write(1) // finishes the checkpoint
	write(1) // starts the WAL over
	if f := walFrames(); f > walLongFrames {
		t.Fatalf("WAL still has %d frames", f)
	}
	if s.walLong.Load() {
		t.Fatal("walLong not cleared")
	}
}

func TestSessionsActiveSinceAndMeta(t *testing.T) {
	s, _ := openTemp(t)
	now := time.Now()
	tx, _ := s.Begin(ctx)
	tx.PutSession(ctx, &engine.Session{ID: "a:old", RootID: "a:old", Agent: "a", StartedAt: now.Add(-48 * time.Hour), LastEventAt: now.Add(-48 * time.Hour)})
	tx.PutSession(ctx, &engine.Session{ID: "a:new", RootID: "a:new", Agent: "a", StartedAt: now.Add(-time.Hour), LastEventAt: now})
	tx.Commit()
	list, err := s.SessionsActiveSince(ctx, now.Add(-24*time.Hour))
	if err != nil || len(list) != 1 || list[0].ID != "a:new" {
		t.Fatalf("%v %v", list, err)
	}
	if err := s.SetMeta(ctx, "k", "v"); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Meta(ctx, "k"); v != "v" {
		t.Fatalf("meta: %q", v)
	}
}
