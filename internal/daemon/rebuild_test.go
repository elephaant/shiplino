// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package daemon

import (
	"database/sql"
	"strconv"
	"testing"

	"github.com/elephaant/shiplino/pkg/engine"
)

// Sessions built by an older engine are rebuilt from their events once.
func TestRebuildOnEngineChange(t *testing.T) {
	e := newEnv(t)
	e.hookFixture()
	e.poll()
	want := e.session(sid)
	if rev, _ := e.st.Meta(ctx, "engine_rev"); rev != strconv.Itoa(engine.Rev) {
		t.Fatalf("engine_rev = %q after first start", rev)
	}

	// Simulate sessions written by an older engine.
	db, err := sql.Open("sqlite", e.db)
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(`UPDATE meta SET value = '1' WHERE key = 'engine_rev'`)
	db.Exec(`UPDATE sessions SET body = json_set(body, '$.turns', 0, '$.active_ms', 0) WHERE id = ?`, sid)
	db.Close()

	e.restart()
	got := e.session(sid)
	if got.Turns != want.Turns || got.ActiveMS != want.ActiveMS || got.ToolCalls != want.ToolCalls || got.BestCostUSD != want.BestCostUSD {
		t.Fatalf("rebuilt session differs:\n got %+v\nwant %+v", got, want)
	}
	if rev, _ := e.st.Meta(ctx, "engine_rev"); rev != strconv.Itoa(engine.Rev) {
		t.Fatalf("engine_rev = %q after rebuild", rev)
	}
	// Events are processed normally afterwards (nothing double counted).
	e.poll()
	if s := e.session(sid); s.Turns != want.Turns {
		t.Fatalf("turns after poll: %d", s.Turns)
	}
}
