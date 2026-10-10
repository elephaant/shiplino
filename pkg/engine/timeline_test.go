// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package engine

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/elephaant/shiplino/pkg/model"
)

func TestSegments(t *testing.T) {
	t0 := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	at := func(min int) time.Time { return t0.Add(time.Duration(min) * time.Minute) }
	mk := func(min int, kind model.Kind) Mark { return Mark{TS: at(min), Kind: kind, Actor: "s"} }
	root := func(status Status, last int, ended bool) *Session {
		s := &Session{ID: "s", Status: status, StartedAt: t0, LastEventAt: at(last)}
		if ended {
			s.EndedAt = at(last)
		}
		return s
	}
	sub := &Session{ID: "s/sub:a", ParentID: "s", Status: StatusDone, StartedAt: at(5), LastEventAt: at(9), EndedAt: at(12)}

	tests := []struct {
		name  string
		s     *Session
		marks []Mark
		now   int
		want  string
	}{
		{"turns with an idle gap", root(StatusDone, 30, true), []Mark{
			mk(1, model.KindTurnStart), mk(5, model.KindToolStart), mk(10, model.KindTurnEnd),
			mk(20, model.KindTurnStart), mk(30, model.KindTurnEnd), mk(30, model.KindSessionEnd),
		}, 60, "idle 0-1 running 1-10 idle 10-20 running 20-30"},
		{"waiting until the next activity", root(StatusReview, 20, false), []Mark{
			mk(0, model.KindTurnStart), mk(4, model.KindWaitingStart), mk(9, model.KindToolEnd), mk(20, model.KindTurnEnd),
		}, 60, "running 0-4 waiting 4-9 running 9-20"},
		{"still waiting extends to now", root(StatusWaiting, 10, false), []Mark{
			mk(0, model.KindTurnStart), mk(10, model.KindWaitingStart),
		}, 25, "running 0-10 waiting 10-25"},
		{"running extends to now", root(StatusRunning, 3, false), []Mark{mk(0, model.KindTurnStart)}, 7, "running 0-7"},
		{"running but quiet past IdleAfter stops at its last event", root(StatusRunning, 3, false), []Mark{mk(0, model.KindTurnStart)}, 40, "running 0-3"},
		{"idle session stops at its last event", root(StatusIdle, 3, false), []Mark{mk(0, model.KindTurnStart)}, 90, "running 0-3"},
		{"joined mid-turn", root(StatusDone, 8, false), []Mark{mk(8, model.KindTurnEnd)}, 60, "running 0-8"},
		{"no turn events at all", root(StatusDone, 8, false), []Mark{mk(2, model.KindToolStart)}, 60, "running 0-8"},
		{"other actors are skipped", root(StatusDone, 10, true), []Mark{
			mk(0, model.KindTurnStart), {TS: at(3), Kind: model.KindWaitingStart, Actor: "s/sub:a"}, mk(10, model.KindSessionEnd),
		}, 60, "running 0-10"},
		{"subagent runs to its end and has its own waiting", sub, []Mark{
			{TS: at(5), Kind: model.KindToolStart, Actor: "s/sub:a"},
			{TS: at(7), Kind: model.KindWaitingStart, Actor: "s/sub:a"},
			{TS: at(9), Kind: model.KindToolEnd, Actor: "s/sub:a"},
			{TS: at(10), Kind: model.KindTurnEnd, Actor: "s"},
			{TS: at(12), Kind: model.KindSubagentEnd, Actor: "s", Child: "s/sub:a"},
			{TS: at(15), Kind: model.KindToolStart, Actor: "s/sub:a"},
		}, 60, "running 5-7 waiting 7-9 running 9-12"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := render(Segments(tt.s, tt.marks, at(tt.now)), t0); got != tt.want {
				t.Errorf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}

func TestClip(t *testing.T) {
	t0 := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	at := func(min int) time.Time { return t0.Add(time.Duration(min) * time.Minute) }
	segs := []Segment{{at(0), at(10), SegRunning}, {at(10), at(20), SegIdle}, {at(20), at(30), SegRunning}}
	if got := render(Clip(segs, at(5), at(20)), t0); got != "running 5-10 idle 10-20" {
		t.Errorf("got %s", got)
	}
	if got := Clip(segs, at(40), at(50)); got != nil {
		t.Errorf("outside the window: %v", got)
	}
}

func render(segs []Segment, t0 time.Time) string {
	var parts []string
	for _, g := range segs {
		parts = append(parts, fmt.Sprintf("%s %d-%d", g.State, int(g.Start.Sub(t0).Minutes()), int(g.End.Sub(t0).Minutes())))
	}
	return strings.Join(parts, " ")
}
