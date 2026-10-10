// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package engine

import (
	"time"

	"github.com/elephaant/shiplino/pkg/model"
)

// Timeline states. A session is "running" inside a turn (prompt to
// answer), "waiting" while it needs the user, and "idle" between turns.
const (
	SegRunning = "running"
	SegWaiting = "waiting"
	SegIdle    = "idle"
)

// Segment is a stretch of time a session spent in one state.
type Segment struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	State string    `json:"state"`
}

// Mark is the part of a stored event the timeline needs.
type Mark struct {
	TS    time.Time
	Kind  model.Kind
	Actor string // actor id (the session or subagent the event belongs to)
	Child string // subagent.start/end: the subagent's id
}

// MarkKinds are the event kinds Segments reads; callers load only these.
var MarkKinds = []model.Kind{
	model.KindTurnStart, model.KindTurnEnd, model.KindWaitingStart, model.KindWaitingEnd,
	model.KindToolStart, model.KindToolEnd, model.KindSessionEnd, model.KindSubagentEnd,
}

// Segments splits a session's life into running, waiting and idle
// segments, from the marks of its root session in time order (marks of
// other actors are skipped). It follows the same rules as Apply: a turn
// runs until turn.end, waiting lasts until the next activity, and a
// session still running or waiting extends to now. Adjacent segments in
// the same state are merged. A running session quiet for longer than
// MarkIdle allows stops at its last event.
func Segments(s *Session, marks []Mark, now time.Time) []Segment {
	end := s.LastEventAt
	switch {
	case !s.EndedAt.IsZero():
		end = s.EndedAt
	case s.Status == StatusWaiting:
		end = now
	case s.Status == StatusRunning:
		// Until MarkIdle catches up, a quiet session is already idle.
		after := IdleAfter
		if s.InFlight > 0 {
			after = IdleAfterInFlight
		}
		if now.Sub(s.LastEventAt) < after {
			end = now
		}
	}
	if end.Before(s.StartedAt) {
		end = s.StartedAt
	}

	// A subagent works from start to end. A session is idle until its
	// first prompt, unless its first turn mark is a turn.end (Shiplino
	// joined mid-turn) or it reports no turns at all.
	state := SegRunning
	if s.ParentID == "" {
		for _, m := range marks {
			if m.Actor != s.ID {
				continue
			}
			if m.Kind == model.KindTurnStart {
				state = SegIdle
				break
			}
			if m.Kind == model.KindTurnEnd {
				break
			}
		}
	}

	var out []Segment
	at := s.StartedAt
	emit := func(to time.Time) {
		if to.After(end) {
			to = end
		}
		if !to.After(at) {
			return
		}
		if n := len(out); n > 0 && out[n-1].State == state && !out[n-1].End.Before(at) {
			out[n-1].End = to
		} else {
			out = append(out, Segment{Start: at, End: to, State: state})
		}
		at = to
	}
	for _, m := range marks {
		if m.TS.Before(at) {
			// Before the session started (or out of order): it only
			// changes the state from here on.
			m.TS = at
		}
		var next string
		switch {
		case m.Kind == model.KindSubagentEnd && m.Child == s.ID:
			emit(m.TS)
			return out
		case m.Actor != s.ID:
			continue
		case m.Kind == model.KindSessionEnd:
			emit(m.TS)
			return out
		case m.Kind == model.KindTurnStart, m.Kind == model.KindWaitingEnd:
			next = SegRunning
		case m.Kind == model.KindToolStart, m.Kind == model.KindToolEnd:
			next = SegRunning // activity ends waiting, and implies a turn
		case m.Kind == model.KindWaitingStart:
			next = SegWaiting
		case m.Kind == model.KindTurnEnd:
			next = SegIdle
			if s.ParentID != "" {
				next = SegRunning // subagents don't have turns of their own
			}
		default:
			continue
		}
		if next != state {
			emit(m.TS)
			state = next
		}
	}
	emit(end)
	return out
}

// Clip limits segments to [from, to).
func Clip(segs []Segment, from, to time.Time) []Segment {
	var out []Segment
	for _, g := range segs {
		if !g.End.After(from) || !g.Start.Before(to) {
			continue
		}
		if g.Start.Before(from) {
			g.Start = from
		}
		if g.End.After(to) {
			g.End = to
		}
		out = append(out, g)
	}
	return out
}
