// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package pricing

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
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
}

// Table is a price table.
type Table struct {
	Checked string  `json:"checked"`
	Source  string  `json:"source"`
	Models  []Model `json:"models"`
}

// Usage is the token usage of one model request.
type Usage struct {
	Input        int64 // uncached input tokens
	Output       int64
	CacheRead    int64
	CacheWrite5m int64
	CacheWrite1h int64
}

// Prompt is every input token of the request.
func (u Usage) Prompt() int64 { return u.Input + u.CacheRead + u.CacheWrite5m + u.CacheWrite1h }

// Default is the table bundled with this release.
var Default = mustParse(bundled)

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
		if rest := model[len(m.ID):]; rest != "" && !strings.ContainsAny(rest[:1], "-@[:") {
			continue
		}
		if best == nil || len(m.ID) > len(best.ID) {
			best = m
		}
	}
	return best, best != nil
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
	usd := float64(u.Input)*r.Input +
		float64(u.Output)*r.Output +
		float64(u.CacheRead)*r.CacheRead +
		float64(u.CacheWrite5m)*r.CacheWrite5m +
		float64(u.CacheWrite1h)*r.CacheWrite1h
	return usd / 1e6, true
}
