// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package pricing

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

//go:embed prices.json
var bundled []byte

// Rates are USD per million tokens.
type Rates struct {
	Input        float64 `json:"input"`
	Output       float64 `json:"output"`
	CacheWrite5m float64 `json:"cache_write_5m"`
	CacheWrite1h float64 `json:"cache_write_1h"`
	CacheRead    float64 `json:"cache_read"`
}

// LongContext switches to other rates when a request's prompt (all input
// tokens, including cache reads and writes) exceeds OverPromptTokens.
type LongContext struct {
	OverPromptTokens int64 `json:"over_prompt_tokens"`
	Rates
}

// Model is one priced model id.
type Model struct {
	ID string `json:"id"`
	Rates
	LongContext *LongContext `json:"long_context,omitempty"`
	// FastMultiplier scales every rate in fast mode (0 = no fast mode).
	FastMultiplier float64 `json:"fast_multiplier,omitempty"`
	// FlexMultiplier scales every rate on the flex/batch tier (0 = n/a).
	FlexMultiplier float64 `json:"flex_multiplier,omitempty"`
	// USGeoMultiplier scales every rate for US-only inference (0 = n/a).
	USGeoMultiplier float64 `json:"us_geo_multiplier,omitempty"`
}

// Fees are per-use charges on top of tokens.
type Fees struct {
	WebSearchPer1K float64 `json:"web_search_per_1k"`
}

// Table is a price table.
type Table struct {
	Checked string   `json:"checked"`
	Source  string   `json:"source"`
	Sources []string `json:"sources,omitempty"`
	Fees    Fees     `json:"fees"`
	Models  []Model  `json:"models"`
}

// Usage is the token usage of one model request.
type Usage struct {
	Input        int64 // uncached input tokens
	Output       int64
	CacheRead    int64
	CacheWrite5m int64
	CacheWrite1h int64
	WebSearches  int64  // server-side web searches ($ per 1,000)
	Speed        string // "fast" (fast mode / priority) or "flex"; anything else is standard
	InferenceGeo string // "us" for US-only inference; anything else is global
}

// Prompt is every input token of the request.
func (u Usage) Prompt() int64 { return u.Input + u.CacheRead + u.CacheWrite5m + u.CacheWrite1h }

// Default returns the table bundled with this release, parsed on first
// use (the hook binary never pays for it).
var Default = sync.OnceValue(func() *Table { return mustParse(bundled) })

func mustParse(b []byte) *Table {
	t, err := Parse(b)
	if err != nil {
		panic(fmt.Sprintf("pricing: bundled prices.json: %v", err))
	}
	return t
}

// Parse reads a price table.
func Parse(b []byte) (*Table, error) {
	var t Table
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, err
	}
	for _, m := range t.Models {
		if m.ID == "" || m.Input <= 0 || m.Output <= 0 {
			return nil, fmt.Errorf("model %q: missing id or prices", m.ID)
		}
	}
	return &t, nil
}

// Lookup finds the rates for a model id as it appears in transcripts. It
// tolerates provider prefixes ("us.anthropic.claude-…"), date suffixes
// ("claude-haiku-4-5-20251001") and variant tags ("claude-opus-5-5[1m]")
// by picking the longest known id that matches on a boundary.
func (t *Table) Lookup(model string) (*Model, bool) {
	if i := strings.Index(model, "claude-"); i > 0 {
		model = model[i:]
	}
	var best *Model
	for i := range t.Models {
		m := &t.Models[i]
		if !strings.HasPrefix(model, m.ID) {
			continue
		}
		if rest := model[len(m.ID):]; rest != "" && !suffixOK(rest) {
			continue
		}
		if best == nil || len(m.ID) > len(best.ID) {
			best = m
		}
	}
	return best, best != nil
}

// suffixOK accepts what may follow a known id in the same model: a date
// ("-20251001", "-2026-01-15"), a variant tag ("[1m]") or a provider
// version ("@…", ":…"). "-mini" and the like are different models.
func suffixOK(rest string) bool {
	switch rest[0] {
	case '@', '[', ':':
		return true
	case '-':
		return len(rest) > 1 && rest[1] >= '0' && rest[1] <= '9'
	}
	return false
}

// Cost returns the USD cost of u on model, and false if the model is unknown.
func (t *Table) Cost(model string, u Usage) (float64, bool) {
	m, ok := t.Lookup(model)
	if !ok {
		return 0, false
	}
	r := m.Rates
	if lc := m.LongContext; lc != nil && u.Prompt() > lc.OverPromptTokens {
		r = lc.Rates
	}
	mult := 1.0
	switch {
	case u.Speed == "fast" && m.FastMultiplier > 0:
		mult *= m.FastMultiplier
	case u.Speed == "flex" && m.FlexMultiplier > 0:
		mult *= m.FlexMultiplier
	}
	if u.InferenceGeo == "us" && m.USGeoMultiplier > 0 {
		mult *= m.USGeoMultiplier
	}
	usd := float64(u.Input)*r.Input +
		float64(u.Output)*r.Output +
		float64(u.CacheRead)*r.CacheRead +
		float64(u.CacheWrite5m)*r.CacheWrite5m +
		float64(u.CacheWrite1h)*r.CacheWrite1h
	return usd/1e6*mult + float64(u.WebSearches)*t.Fees.WebSearchPer1K/1000, true
}
