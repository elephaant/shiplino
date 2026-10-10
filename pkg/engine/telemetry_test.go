// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

package engine

import (
	"testing"

	"github.com/elephaant/shiplino/pkg/model"
)

// Telemetry (OTLP) usage describes the same requests as transcript usage:
// it never adds to it, whatever order the two arrive in.
func TestTelemetryUsageIsComparedNotAdded(t *testing.T) {
	otlpUsage := func(sec int, cost float64) model.Event {
		e := ev(sec, model.KindUsage, map[string]any{"model": "claude-haiku-5-5", "input_tokens": 100.0, "output_tokens": 10.0, "cost_usd": cost, "cost_source": "reported"})
		e.Collector = model.CollectorOTLP
		return e
	}
	transcript := func(sec int, cost float64) model.Event {
		e := ev(sec, model.KindUsage, map[string]any{"model": "claude-opus-5", "input_tokens": 100.0, "output_tokens": 10.0, "cost_usd": cost})
		e.Collector = model.CollectorTranscript
		return e
	}
	for _, tc := range []struct {
		name   string
		events []model.Event
	}{
		{"telemetry first", []model.Event{otlpUsage(1, 0.5), otlpUsage(2, 0.25), transcript(3, 0.4)}},
		{"transcript first", []model.Event{transcript(1, 0.4), otlpUsage(2, 0.5), otlpUsage(3, 0.25)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := New(nil, nil)
			for _, x := range tc.events {
				e.Apply(x)
			}
			s := e.Get(sid)
			if s.InputTokens != 100 || s.OutputTokens != 10 || s.TokensSource != "" {
				t.Errorf("tokens = %d/%d from %q, want the transcript's 100/10", s.InputTokens, s.OutputTokens, s.TokensSource)
			}
			if s.Telemetry == nil || s.Telemetry.Requests != 2 || s.Telemetry.InputTokens != 200 || s.Telemetry.CostUSD != 0.75 {
				t.Errorf("telemetry = %+v", s.Telemetry)
			}
			if s.BestCostUSD != 0.75 || s.CostSource != "reported" || s.CostUSD != 0.4 {
				t.Errorf("cost best=%v (%s) computed=%v", s.BestCostUSD, s.CostSource, s.CostUSD)
			}
			if s.Model != "claude-opus-5" {
				t.Errorf("model = %q: a background model from telemetry replaced the session's", s.Model)
			}
		})
	}

	// Telemetry only: its tokens stand in, and it is the reported cost.
	e := New(nil, nil)
	e.Apply(otlpUsage(1, 0.5))
	s := e.Get(sid)
	if s.InputTokens != 100 || s.TokensSource != "telemetry" || s.BestCostUSD != 0.5 || s.CostSource != "reported" || s.Model != "claude-haiku-5-5" {
		t.Fatalf("telemetry only: %+v", s)
	}
	// The agent's running total and telemetry count the same calls: max, not sum.
	e.Apply(ev(2, model.KindUsage, map[string]any{"report": true, "process": "p1", "total_cost_usd": 0.6}))
	if s.BestCostUSD != 0.6 {
		t.Fatalf("best = %v, want max(0.6 reported, 0.5 telemetry)", s.BestCostUSD)
	}
}

func TestTelemetryActivityOnlyFillsGaps(t *testing.T) {
	otlp := func(kind model.Kind) model.Event {
		e := ev(5, kind, nil)
		e.Collector = model.CollectorOTLP
		return e
	}
	for _, tc := range []struct {
		first     model.Collector
		redundant bool
	}{
		{model.CollectorHook, true},
		{model.CollectorTranscript, true},
		{model.CollectorOTLP, false},
	} {
		e := New(nil, nil)
		first := ev(1, model.KindTurnStart, map[string]any{"prompt": "hi"})
		first.Collector = tc.first
		e.Apply(first)
		if got := e.Redundant(otlp(model.KindTurnStart)); got != tc.redundant {
			t.Errorf("after %s activity: redundant = %v", tc.first, got)
		}
		if e.Redundant(otlp(model.KindUsage)) {
			t.Errorf("usage is never redundant")
		}
	}
}
