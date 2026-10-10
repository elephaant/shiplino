// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package engine

import (
	"testing"
	"time"

	"github.com/elephaant/shiplino/pkg/model"
)

func TestUsageNone(t *testing.T) {
	e := New(nil, nil)
	sub := func(sec int, kind model.Kind, data map[string]any) model.Event {
		x := ev(sec, kind, data)
		x.ActorID, x.ParentActor = sid+"/sub:a", sid
		return x
	}
	e.Apply(ev(0, model.KindTurnStart, map[string]any{"prompt": "go"}))
	if u := e.Get(sid).Usage; u != "" {
		t.Fatalf("running session: usage %q", u)
	}
	e.Apply(ev(1, model.KindToolStart, map[string]any{"tool": "read"}))
	e.Apply(ev(2, model.KindTurnEnd, map[string]any{"status": "ok"}))
	if u := e.Get(sid).Usage; u != UsageNone {
		t.Fatalf("settled without usage: %q", u)
	}
	// Working again: undecided until it settles.
	e.Apply(ev(3, model.KindTurnStart, map[string]any{"prompt": "more"}))
	if u := e.Get(sid).Usage; u != "" {
		t.Fatalf("running again: %q", u)
	}
	// A subagent's usage counts for its parent, even unpriced.
	e.Apply(sub(4, model.KindUsage, map[string]any{"input_tokens": 10.0, "cost_source": "unpriced"}))
	e.Apply(ev(5, model.KindTurnEnd, map[string]any{"status": "ok"}))
	if p, c := e.Get(sid).Usage, e.Get(sid+"/sub:a").Usage; p != UsageTokens || c != UsageTokens {
		t.Fatalf("usage parent=%q sub=%q", p, c)
	}

	// Sessions that went quiet after doing work are settled too; sessions
	// that did nothing aren't marked.
	e2 := New(nil, nil)
	e2.Apply(ev(0, model.KindToolStart, map[string]any{"tool": "shell"}))
	e2.Apply(model.Event{TS: t0, Kind: model.KindSessionStart, Agent: model.Agent{Name: "cursor"}, SessionID: "cursor:empty", ActorID: "cursor:empty"})
	e2.MarkIdle(t0.Add(IdleAfterInFlight + time.Minute))
	if u := e2.Get(sid).Usage; u != UsageNone {
		t.Fatalf("idle: %q", u)
	}
	if u := e2.Get("cursor:empty").Usage; u != "" {
		t.Fatalf("empty session: %q", u)
	}
}
