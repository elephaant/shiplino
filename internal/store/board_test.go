// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

package store

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/elephaant/shiplino/pkg/board"
	"github.com/elephaant/shiplino/pkg/engine"
	"github.com/elephaant/shiplino/pkg/projects"
)

// BoardSessions skips old closed work; every sprint's board must come out
// exactly as when built from all of the project's sessions.
func TestBoardSessionsGiveTheSameBoard(t *testing.T) {
	s, _ := openTemp(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	origin := now.Add(-70 * 24 * time.Hour)
	p := projects.Project{ID: "proj", Name: "proj", Kind: "folder"}
	rng := rand.New(rand.NewPCG(1, 2))
	statuses := []engine.Status{engine.StatusDone, engine.StatusDone, engine.StatusFailed, engine.StatusRunning,
		engine.StatusWaiting, engine.StatusIdle, engine.StatusReview}

	tx, _ := s.Begin(ctx)
	tx.PutProject(ctx, p, origin)
	tx.PutProject(ctx, projects.Project{ID: "other", Name: "other", Kind: "folder"}, origin)
	var overrides []board.Override
	for i := range 300 {
		start := origin.Add(time.Duration(rng.Int64N(int64(now.Sub(origin)))))
		last := start.Add(time.Duration(rng.Int64N(int64(30 * time.Hour))))
		if last.After(now) {
			last = now
		}
		root := &engine.Session{ID: fmt.Sprintf("claude-code:r%03d", i), Agent: "claude-code", ProjectID: p.ID,
			Status: statuses[rng.IntN(len(statuses))], StartedAt: start, LastEventAt: last}
		root.RootID = root.ID
		if root.Status == engine.StatusDone || root.Status == engine.StatusFailed {
			root.EndedAt = last.Add(-time.Duration(rng.Int64N(int64(time.Hour)))) // late events can follow the end
		}
		if rng.IntN(3) == 0 {
			root.Files = []string{"a.go"}
		}
		if rng.IntN(10) == 0 {
			root.ProjectID = "other"
		}
		tx.PutSession(ctx, root)
		for k := range rng.IntN(3) {
			kid := &engine.Session{ID: fmt.Sprintf("%s/sub:%d", root.ID, k), Agent: "claude-code", ParentID: root.ID, RootID: root.ID,
				ProjectID: root.ProjectID, Status: statuses[rng.IntN(len(statuses))], StartedAt: start, LastEventAt: start}
			tx.PutSession(ctx, kid)
		}
		if rng.IntN(8) == 0 {
			col := []string{"", board.Backlog, board.Review, board.Done, board.Failed}[rng.IntN(5)]
			overrides = append(overrides, board.Override{CardID: root.ID, ProjectID: p.ID, Origin: board.OriginAuto, Column: col, Notes: "n", CreatedAt: start})
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	overrides = append(overrides, board.Override{CardID: "card:m1", ProjectID: p.ID, Origin: board.OriginManual, Title: "todo", CreatedAt: origin})
	for _, o := range overrides {
		if err := s.PutOverride(ctx, o); err != nil {
			t.Fatal(err)
		}
	}

	all, err := s.SessionsIn(ctx, p.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	ov, err := s.Overrides(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	cal := board.Calendar{Origin: origin}
	key := func(cards []board.Card) []string {
		var out []string
		for _, c := range cards {
			out = append(out, fmt.Sprintf("%s %s %d %d %d %s", c.ID, c.Column, c.Sprint, len(c.Subagents), c.ToolCalls, c.Status))
		}
		slices.Sort(out)
		return out
	}
	for n := 1; n <= cal.Number(now); n++ {
		var since time.Time
		if n > 1 {
			since = cal.Get(n).Starts
		}
		some, err := s.BoardSessions(ctx, p.ID, since)
		if err != nil {
			t.Fatal(err)
		}
		want := key(board.FilterSprint(board.Build(all, ov, cal, now), n))
		got := key(board.FilterSprint(board.Build(some, ov, cal, now), n))
		if !slices.Equal(got, want) {
			t.Fatalf("sprint %d: board from %d of %d sessions differs:\ngot  %v\nwant %v", n, len(some), len(all), got, want)
		}
		if n == cal.Number(now) && len(some) >= len(all) {
			t.Fatalf("current sprint read every session (%d)", len(some))
		}
	}
}
