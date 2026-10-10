// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package pricing

import (
	"math"
	"strings"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestLookup(t *testing.T) {
	cases := map[string]string{
		"claude-opus-5-5":                "claude-opus-5-5",
		"claude-opus-5-5[1m]":            "claude-opus-5-5",
		"claude-opus-5":                  "claude-opus-5",
		"claude-opus-4-8":                "claude-opus-4-8",
		"claude-opus-4-20250514":         "claude-opus-4",
		"claude-opus-4-1-20250805":       "claude-opus-4-1",
		"claude-sonnet-4-5-20250929":     "claude-sonnet-4-5",
		"claude-haiku-4-5-20251001":      "claude-haiku-4-5",
		"us.anthropic.claude-sonnet-5-5": "claude-sonnet-5-5",
		"claude-opus-4-5@20251101":       "claude-opus-4-5",
		"claude-3-5-haiku-20241022":      "claude-3-5-haiku",
		"claude-fable-5-1":               "claude-fable-5-1",
	}
	for in, want := range cases {
		m, ok := Default().Lookup(in)
		if !ok || m.ID != want {
			t.Errorf("Lookup(%q) = %v, want %s", in, m, want)
		}
	}
	for _, in := range []string{"<synthetic>", "gpt-5", "claude-opus-55", "claude-sonnet-4x", ""} {
		if m, ok := Default().Lookup(in); ok {
			t.Errorf("Lookup(%q) = %s, want no match", in, m.ID)
		}
	}
}

func TestCost(t *testing.T) {
	// Opus 5.5: $4 in, $20 out, $5 5m write, $8 1h write, $0.20 read per MTok.
	u := Usage{Input: 1_000_000, Output: 100_000, CacheRead: 2_000_000, CacheWrite5m: 1_000_000, CacheWrite1h: 500_000}
	got, ok := Default().Cost("claude-opus-5-5", u)
	want := 4 + 2 + 0.4 + 5 + 4.0
	if !ok || !near(got, want) {
		t.Fatalf("Cost = %v, %v; want %v", got, ok, want)
	}
	if _, ok := Default().Cost("unknown-model", u); ok {
		t.Fatal("unknown model priced")
	}
}

func TestHaikuLongContextTier(t *testing.T) {
	// At or below 100K prompt tokens (cache reads count): base rates.
	small := Usage{Input: 10_000, CacheRead: 90_000, Output: 1_000}
	got, _ := Default().Cost("claude-haiku-5-5", small)
	if want := (10_000*0.1 + 90_000*0.01 + 1_000*0.5) / 1e6; !near(got, want) {
		t.Fatalf("≤100K: %v, want %v", got, want)
	}
	// One token over: every token at the long-context rates.
	big := Usage{Input: 10_001, CacheRead: 90_000, Output: 1_000}
	got, _ = Default().Cost("claude-haiku-5-5", big)
	if want := (10_001*0.5 + 90_000*0.05 + 1_000*2.5) / 1e6; !near(got, want) {
		t.Fatalf(">100K: %v, want %v", got, want)
	}
}

func TestModifiersAndFees(t *testing.T) {
	base := Usage{Input: 1_000_000, Output: 1_000_000, CacheRead: 1_000_000}
	std, _ := Default().Cost("claude-opus-5-5", base)
	if !near(std, 4+20+0.2) {
		t.Fatalf("standard = %v", std)
	}
	fast := base
	fast.Speed = "fast" // Opus 5.5 fast mode: $8/$40, cache multipliers on top
	if got, _ := Default().Cost("claude-opus-5-5", fast); !near(got, 2*std) {
		t.Fatalf("fast = %v, want %v", got, 2*std)
	}
	us := base
	us.InferenceGeo = "us"
	if got, _ := Default().Cost("claude-opus-5-5", us); !near(got, 1.1*std) {
		t.Fatalf("us = %v, want %v", got, 1.1*std)
	}
	both := fast
	both.InferenceGeo = "us" // modifiers stack
	if got, _ := Default().Cost("claude-opus-5-5", both); !near(got, 2.2*std) {
		t.Fatalf("fast+us = %v", got)
	}
	// Models without fast mode / geo pricing ignore the flags.
	old := Usage{Input: 1_000_000, Speed: "fast", InferenceGeo: "us"}
	if got, _ := Default().Cost("claude-haiku-4-5", old); !near(got, 1) {
		t.Fatalf("haiku 4.5 with flags = %v", got)
	}
	// Web search: $10 per 1,000, on top of tokens.
	ws := Usage{WebSearches: 142}
	if got, _ := Default().Cost("claude-haiku-4-5", ws); !near(got, 1.42) {
		t.Fatalf("web searches = %v", got)
	}
}

func TestBundledTableIsSane(t *testing.T) {
	if Default().Checked == "" || Default().Source == "" || len(Default().Models) < 10 {
		t.Fatalf("table: %+v", Default())
	}
	seen := map[string]bool{}
	for _, m := range Default().Models {
		if seen[m.ID] {
			t.Errorf("duplicate id %s", m.ID)
		}
		seen[m.ID] = true
		if m.Output <= m.Input || m.CacheRead >= m.Input {
			t.Errorf("%s: output must cost more than input, cache reads less: %+v", m.ID, m.Rates)
		}
		switch {
		case strings.HasPrefix(m.ID, "claude-"):
			// Anthropic: 5m writes 1.25x input, 1h writes 2x input.
			if !near(m.CacheWrite5m, m.Input*1.25) || !near(m.CacheWrite1h, m.Input*2) {
				t.Errorf("%s: cache write rates look wrong: %+v", m.ID, m.Rates)
			}
		case strings.HasPrefix(m.ID, "gpt-"):
			// OpenAI: cached input is a tenth of input; writes (if billed)
			// cost more than input; long context costs more.
			if !near(m.CacheRead, m.Input/10) || m.CacheWrite5m != 0 && m.CacheWrite5m <= m.Input {
				t.Errorf("%s: cache rates look wrong: %+v", m.ID, m.Rates)
			}
			if lc := m.LongContext; lc == nil && m.ID != "gpt-5.3-codex" || lc != nil && (lc.OverPromptTokens != 272000 || lc.Input <= m.Input) {
				t.Errorf("%s: long context: %+v", m.ID, lc)
			}
		default:
			t.Errorf("%s: unknown provider; add its structure check", m.ID)
		}
	}
	if _, err := Parse([]byte(`{"models":[{"id":"x"}]}`)); err == nil {
		t.Error("table with missing prices accepted")
	}
}

func TestOpenAIRates(t *testing.T) {
	// 100K uncached + 900K cached... is long context; keep it short here.
	cost, ok := Default().Cost("gpt-5.6-terra", Usage{Input: 100_000, CacheRead: 100_000, Output: 10_000})
	if !ok || !near(cost, 0.1*2+0.1*0.2+0.01*12) {
		t.Fatalf("terra short: %v %v", cost, ok)
	}
	// Over 272K prompt tokens: long-context rates.
	cost, _ = Default().Cost("gpt-5.6-terra", Usage{Input: 200_000, CacheRead: 100_000, Output: 10_000})
	if !near(cost, 0.2*4+0.1*0.4+0.01*18) {
		t.Fatalf("terra long: %v", cost)
	}
	fast, _ := Default().Cost("gpt-5.5", Usage{Input: 1000, Output: 1000, Speed: "fast"})
	std, _ := Default().Cost("gpt-5.5", Usage{Input: 1000, Output: 1000})
	flex, _ := Default().Cost("gpt-5.5", Usage{Input: 1000, Output: 1000, Speed: "flex"})
	if !near(fast, std*2.5) || !near(flex, std*0.5) {
		t.Fatalf("tiers: std %v fast %v flex %v", std, fast, flex)
	}
	if _, ok := Default().Cost("codex-auto-review", Usage{Input: 1}); ok {
		t.Fatal("unlisted models must stay unpriced")
	}
}

func TestLookupSuffixes(t *testing.T) {
	for model, want := range map[string]string{
		"claude-haiku-5-5-20251001":                   "claude-haiku-5-5",
		"claude-opus-5-5[1m]":                         "claude-opus-5-5",
		"us.anthropic.claude-haiku-5-5-20251001-v1:0": "claude-haiku-5-5",
		"gpt-5.6-terra":                               "gpt-5.6-terra",
		"gpt-5.6-terra-2026-09-01":                    "gpt-5.6-terra",
		"gpt-5.6-terra-mini":                          "",
		"gpt-5.5-pro":                                 "",
	} {
		m, ok := Default().Lookup(model)
		if got := ""; ok {
			got = m.ID
			if got != want {
				t.Errorf("Lookup(%q) = %q, want %q", model, got, want)
			}
		} else if want != "" {
			t.Errorf("Lookup(%q) found nothing, want %q", model, want)
		}
	}
}
