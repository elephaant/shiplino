// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

package pricing

import (
	"math"
	"strings"
	"testing"
	"time"
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
		checkRates(t, m.ID, "", m.Rates, m.LongContext)
		for _, d := range m.Schedule {
			checkRates(t, m.ID, d.From, d.Rates, d.LongContext)
			if d.Input < m.Input {
				t.Errorf("%s from %s: a price cut? check the source", m.ID, d.From)
			}
		}
	}
	if _, err := Parse([]byte(`{"models":[{"id":"x"}]}`)); err == nil {
		t.Error("table with missing prices accepted")
	}
}

// checkRates applies each provider's price structure to one set of rates
// (a model's own, or a dated entry's).
func checkRates(t *testing.T, id, from string, r Rates, lc *LongContext) {
	t.Helper()
	name := id
	if from != "" {
		name += " from " + from
	}
	if r.Output <= r.Input || r.CacheRead >= r.Input {
		t.Errorf("%s: output must cost more than input, cache reads less: %+v", name, r)
	}
	switch {
	case strings.HasPrefix(id, "claude-"):
		// Anthropic: 5m writes 1.25x input, 1h writes 2x input.
		if !near(r.CacheWrite5m, r.Input*1.25) || !near(r.CacheWrite1h, r.Input*2) {
			t.Errorf("%s: cache write rates look wrong: %+v", name, r)
		}
	case strings.HasPrefix(id, "gpt-"):
		// OpenAI: cached input is a tenth of input; writes (if billed)
		// cost more than input; long context costs more.
		if !near(r.CacheRead, r.Input/10) || r.CacheWrite5m != 0 && r.CacheWrite5m <= r.Input {
			t.Errorf("%s: cache rates look wrong: %+v", name, r)
		}
		if lc == nil && id != "gpt-5.3-codex" || lc != nil && (lc.OverPromptTokens != 272000 || lc.Input <= r.Input) {
			t.Errorf("%s: long context: %+v", name, lc)
		}
	case strings.HasPrefix(id, "gemini-"):
		// Google: cached input is a tenth of input; long context is a
		// prompt over 200K tokens and costs more.
		if !near(r.CacheRead, r.Input/10) || r.CacheWrite5m != 0 {
			t.Errorf("%s: cache rates look wrong: %+v", name, r)
		}
		if lc != nil && (lc.OverPromptTokens != 200000 || lc.Input <= r.Input || lc.Output <= r.Output) {
			t.Errorf("%s: long context: %+v", name, lc)
		}
	default:
		t.Errorf("%s: unknown provider; add its structure check", name)
	}
}

func TestDatedPrices(t *testing.T) {
	// gemini-3.8-flash: $0.75 in / $3.75 out / $0.075 cached through
	// 2026-12-31, then $1.50 / $7.50 / $0.15.
	u := Usage{Input: 1_000_000, Output: 1_000_000, CacheRead: 1_000_000}
	for at, want := range map[string]float64{
		"2026-10-10T12:00:00Z":      4.575,
		"2026-12-31T23:59:59Z":      4.575,
		"2027-01-01T00:00:00Z":      9.15,
		"2027-06-01T00:00:00Z":      9.15,
		"2026-12-31T20:00:00-05:00": 9.15, // 01:00 UTC on the 1st
	} {
		u.At, _ = time.Parse(time.RFC3339, at)
		if got, ok := Default().Cost("gemini-3.8-flash", u); !ok || !near(got, want) {
			t.Errorf("at %s: %v, want %v", at, got, want)
		}
	}
	// No time means now.
	u.At = time.Time{}
	now := u
	now.At = time.Now()
	want, _ := Default().Cost("gemini-3.8-flash", now)
	if got, _ := Default().Cost("gemini-3.8-flash", u); !near(got, want) {
		t.Errorf("zero At: %v, want %v", got, want)
	}

	tab, err := Parse([]byte(`{"models":[{"id":"m","input":1,"output":2,"schedule":[
		{"from":"2027-01-01","input":2,"output":4},{"from":"2028-01-01","input":3,"output":6}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	for at, want := range map[string]float64{"2026-06-01": 3, "2027-06-01": 6, "2029-01-01": 9} {
		ts, _ := time.Parse(time.DateOnly, at)
		if got, _ := tab.Cost("m", Usage{Input: 1e6, Output: 1e6, At: ts}); !near(got, want) {
			t.Errorf("two steps at %s: %v, want %v", at, got, want)
		}
	}
	for name, bad := range map[string]string{
		"bad date":     `{"models":[{"id":"m","input":1,"output":2,"schedule":[{"from":"Jan 2027","input":2,"output":4}]}]}`,
		"no prices":    `{"models":[{"id":"m","input":1,"output":2,"schedule":[{"from":"2027-01-01"}]}]}`,
		"out of order": `{"models":[{"id":"m","input":1,"output":2,"schedule":[{"from":"2028-01-01","input":2,"output":4},{"from":"2027-01-01","input":3,"output":6}]}]}`,
		"lost long context": `{"models":[{"id":"m","input":1,"output":2,"long_context":{"over_prompt_tokens":10,"input":2,"output":4},
			"schedule":[{"from":"2027-01-01","input":2,"output":4}]}]}`,
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestGeminiRates(t *testing.T) {
	// gemini-2.5-pro: $1.25 in, $10 out, $0.125 cached; over 200K: $2.50, $15, $0.25.
	cost, ok := Default().Cost("gemini-2.5-pro", Usage{Input: 100_000, CacheRead: 100_000, Output: 10_000})
	if !ok || !near(cost, 0.125+0.0125+0.1) {
		t.Fatalf("standard: %v %v", cost, ok)
	}
	cost, _ = Default().Cost("gemini-2.5-pro", Usage{Input: 150_000, CacheRead: 100_000, Output: 10_000})
	if !near(cost, 0.375+0.025+0.15) {
		t.Fatalf("long context: %v", cost)
	}
	for model, want := range map[string]string{"gemini-2.5-flash": "gemini-2.5-flash", "gemini-2.5-flash-lite": "gemini-2.5-flash-lite", "gemini-3.1-pro-preview-customtools": "gemini-3.1-pro-preview-customtools", "gemini-9-ultra": ""} {
		m, _ := Default().Lookup(model)
		if got := ""; m != nil && m.ID != want || m == nil && want != "" {
			if m != nil {
				got = m.ID
			}
			t.Errorf("Lookup(%q) = %q, want %q", model, got, want)
		}
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
