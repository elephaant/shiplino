package board

import (
	"errors"
	"testing"
	"time"

	"github.com/elephaant/shiplino/pkg/engine"
)

var utc = time.UTC

// Project first seen on Wednesday 2026-10-07; sprint 1 = Mon Oct 5 – Sun Oct 11.
var cal = Calendar{Origin: time.Date(2026, 10, 7, 15, 0, 0, 0, utc), Loc: utc}

func at(y int, m time.Month, d, h int) time.Time { return time.Date(y, m, d, h, 0, 0, 0, utc) }

func TestSprintNumbers(t *testing.T) {
	cases := []struct {
		t    time.Time
		want int
	}{
		{at(2026, 10, 1, 9), 1},   // before the origin
		{at(2026, 10, 5, 0), 1},   // Monday start
		{at(2026, 10, 11, 23), 1}, // Sunday night
		{at(2026, 10, 12, 0), 2},  // next Monday
		{at(2026, 11, 2, 12), 5},
	}
	for _, c := range cases {
		if got := cal.Number(c.t); got != c.want {
			t.Errorf("Number(%v) = %d, want %d", c.t, got, c.want)
		}
	}
	s := cal.Get(2)
	if !s.Starts.Equal(at(2026, 10, 12, 0)) || !s.Ends.Equal(at(2026, 10, 19, 0)) || s.Name != "Sprint 2 · Oct 12 – Oct 18" {
		t.Fatalf("Get(2) = %+v", s)
	}
	twoWeek := Calendar{Origin: cal.Origin, Loc: utc, Length: 14, StartDow: 7} // Sundays
	if got := twoWeek.Number(at(2026, 10, 17, 12)); got != 1 {
		t.Fatalf("two-week sprint: %d", got)
	}
	if got := twoWeek.Number(at(2026, 10, 18, 12)); got != 2 {
		t.Fatalf("two-week sprint boundary: %d", got)
	}
}

func TestSprintAcrossDST(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tzdata")
	}
	c := Calendar{Origin: time.Date(2026, 10, 26, 10, 0, 0, 0, ny), Loc: ny} // DST ends Nov 1
	if got := c.Number(time.Date(2026, 11, 2, 0, 30, 0, 0, ny)); got != 2 {
		t.Fatalf("Monday after DST change = sprint %d, want 2", got)
	}
}

func TestRollover(t *testing.T) {
	now := at(2026, 10, 20, 12) // sprint 3
	if s, r := cal.Assign(at(2026, 10, 6, 9), at(2026, 10, 8, 9), true, now); s != 1 || r != 0 {
		t.Errorf("closed in its sprint: %d %d", s, r)
	}
	if s, r := cal.Assign(at(2026, 10, 6, 9), at(2026, 10, 14, 9), true, now); s != 2 || r != 1 {
		t.Errorf("closed next sprint: %d %d", s, r)
	}
	if s, r := cal.Assign(at(2026, 10, 6, 9), time.Time{}, false, now); s != 3 || r != 1 {
		t.Errorf("still open: %d %d", s, r)
	}
}

func sess(id string, st engine.Status, started time.Time) *engine.Session {
	return &engine.Session{ID: id, RootID: id, Agent: "claude-code", Status: st, ProjectID: "p", Title: id, StartedAt: started, LastEventAt: started}
}

func TestBuildAndLayout(t *testing.T) {
	now := at(2026, 10, 9, 12)
	a := sess("a", engine.StatusRunning, at(2026, 10, 9, 9))
	b := sess("b", engine.StatusWaiting, at(2026, 10, 9, 10))
	c := sess("c", engine.StatusReview, at(2026, 10, 8, 10))
	d := sess("d", engine.StatusDone, at(2026, 10, 8, 11))
	d.LastEventAt = at(2026, 10, 9, 11)
	sub := &engine.Session{ID: "a/sub:x", RootID: "a", ParentID: "a", ActorType: "reviewer", Status: engine.StatusRunning, NowDoing: "Reading", CostUSD: 0.1}
	overrides := map[string]Override{
		"c":  {CardID: "c", Origin: OriginAuto, Column: Done, Position: 1},
		"m1": {CardID: "m1", ProjectID: "p", Origin: OriginManual, Title: "Add rate limiting", CreatedAt: at(2026, 10, 1, 9)},
	}
	cards := Build([]*engine.Session{a, b, c, d, sub}, overrides, cal, now)
	cols := Layout(cards)
	byCol := map[string][]string{}
	for _, col := range cols {
		for _, card := range col.Cards {
			byCol[col.ID] = append(byCol[col.ID], card.ID)
		}
	}
	if len(byCol[Backlog]) != 1 || byCol[Running][0] != "a" || byCol[Waiting][0] != "b" || len(byCol[Review]) != 0 {
		t.Fatalf("columns: %v", byCol)
	}
	// c was pinned to Done and sorts before the unpinned d.
	if byCol[Done][0] != "c" || byCol[Done][1] != "d" {
		t.Fatalf("done order: %v", byCol[Done])
	}
	for _, card := range cards {
		if card.ID == "a" && (len(card.Subagents) != 1 || card.Subagents[0].Type != "reviewer") {
			t.Fatalf("subagents not nested: %+v", card.Subagents)
		}
		if card.ID == "m1" && (card.Sprint != 0 || !card.Pinned) {
			t.Fatalf("backlog card: %+v", card)
		}
	}
}

func TestCheckMove(t *testing.T) {
	if err := CheckMove(OriginAuto, Waiting); !errors.Is(err, ErrAgentColumn) {
		t.Fatal("auto card moved to Waiting")
	}
	if err := CheckMove(OriginManual, Running); err != nil {
		t.Fatal(err)
	}
	if err := CheckMove(OriginAuto, "qa"); err == nil {
		t.Fatal("unknown column accepted")
	}
}

func TestFilterSprint(t *testing.T) {
	cards := []Card{{ID: "x", Sprint: 1}, {ID: "y", Sprint: 2}, {ID: "z", Column: Backlog}}
	if got := FilterSprint(cards, 2); len(got) != 2 || got[0].ID != "y" {
		t.Fatalf("%v", got)
	}
}

func TestRollUpAndIdle(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	root := &engine.Session{ID: "a:1", RootID: "a:1", Status: engine.StatusDone, StartedAt: now, LastEventAt: now, Files: []string{"x.go"}, LinesAdded: 3, LinesSource: "agent", ToolCalls: 2,
		PlanTotal: 7, PlanDone: 3}
	kid := &engine.Session{ID: "a:1/sub:k", RootID: "a:1", ParentID: "a:1", ActorType: "reviewer", Status: engine.StatusWaiting, NowDoing: "Approve: rm -rf build",
		Files: []string{"x.go", "y.go"}, LinesAdded: 4, LinesRemoved: 1, LinesSource: "computed", ToolCalls: 5, StartedAt: now, LastEventAt: now, PlanTotal: 2,
		WaitingReason: "permission", WaitingSince: now}
	idle := &engine.Session{ID: "a:2", RootID: "a:2", Status: engine.StatusIdle, StartedAt: now, LastEventAt: now}
	idleWithWork := &engine.Session{ID: "a:3", RootID: "a:3", Status: engine.StatusIdle, Files: []string{"z"}, StartedAt: now, LastEventAt: now}
	cards := Build([]*engine.Session{root, kid, idle, idleWithWork}, nil, Calendar{Loc: time.UTC}, now)
	by := map[string]Card{}
	for _, c := range cards {
		by[c.ID] = c
	}
	c := by["a:1"]
	if c.Column != Waiting || c.Status != engine.StatusWaiting || c.NowDoing != "reviewer: Approve: rm -rf build" || c.WaitingReason != "permission" || !c.WaitingSince.Equal(now) {
		t.Fatalf("waiting subagent should surface on its card: %+v", c)
	}
	if c.Files != 2 || c.LinesAdded != 7 || c.LinesRemoved != 1 || c.ToolCalls != 7 || c.LinesSource != "computed" {
		t.Fatalf("roll-up: files=%d +%d -%d tools=%d src=%q", c.Files, c.LinesAdded, c.LinesRemoved, c.ToolCalls, c.LinesSource)
	}
	if c.PlanTotal != 7 || c.PlanDone != 3 { // the session's own list; a subagent's stays on the subagent
		t.Fatalf("plan: %d/%d", c.PlanDone, c.PlanTotal)
	}
	if by["a:2"].Column != Done || by["a:3"].Column != Review {
		t.Fatalf("idle columns: %s %s", by["a:2"].Column, by["a:3"].Column)
	}
}
