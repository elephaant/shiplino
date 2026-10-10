// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

package claudecode

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/elephaant/shiplino/pkg/adapters"
	"github.com/elephaant/shiplino/pkg/model"
	"github.com/elephaant/shiplino/pkg/pricing"
)

// Transcript format, checked against local Claude Code transcripts on
// 2026-10-10: ~/.claude/projects/<project>/<session>.jsonl, with subagents
// in <session>/subagents/agent-<id>.jsonl. Assistant lines carry
// message.{id, model, usage}, plus sessionId, agentId (subagents only),
// version and timestamp.
//
// One API response is written as several lines (one per content block),
// each repeating the same usage. Usage events are therefore keyed by
// message id so a response is counted exactly once.

type transcriptLine struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionId"`
	AgentID   string `json:"agentId"`
	Version   string `json:"version"`
	Timestamp string `json:"timestamp"`
	CWD       string `json:"cwd"`
	Message   struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *struct {
			InputTokens         int64 `json:"input_tokens"`
			OutputTokens        int64 `json:"output_tokens"`
			CacheCreationTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadTokens     int64 `json:"cache_read_input_tokens"`
			CacheCreation       *struct {
				Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
				Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
			} `json:"cache_creation"`
			ServerToolUse *struct {
				WebSearchRequests int64 `json:"web_search_requests"`
			} `json:"server_tool_use"`
			Speed        string `json:"speed"`
			InferenceGeo string `json:"inference_geo"`
		} `json:"usage"`
	} `json:"message"`

	AITitle string `json:"aiTitle"` // ai-title lines: Claude Code's own session title

	// cost-state lines: Claude Code's own running total for the process.
	TotalCostUSD *float64                  `json:"totalCostUSD"`
	StartTime    int64                     `json:"startTime"`
	ModelUsage   map[string]map[string]any `json:"modelUsage"`
}

// ParseTranscriptLine implements adapters.TranscriptParser.
func (Adapter) ParseTranscriptLine(line []byte, meta adapters.TranscriptMeta) ([]model.Event, error) {
	var l transcriptLine
	if err := json.Unmarshal(line, &l); err != nil {
		return nil, fmt.Errorf("claude-code transcript: %w", err)
	}
	switch l.Type {
	case "cost-state":
		return costReport(l, meta)
	case "ai-title":
		return titleUpdate(l, meta)
	}
	acts := activity(line, meta)
	u := l.Message.Usage
	if l.Type != "assistant" || u == nil || l.Message.ID == "" || l.SessionID == "" || meta.Warmup {
		return acts, nil
	}
	if l.Message.Model == "<synthetic>" { // local error messages, not API calls
		return acts, nil
	}

	ts := meta.ReceivedAt
	if t, err := time.Parse(time.RFC3339Nano, l.Timestamp); err == nil {
		ts = t
	}
	usage := pricing.Usage{
		Input: u.InputTokens, Output: u.OutputTokens, CacheRead: u.CacheReadTokens, CacheWrite5m: u.CacheCreationTokens,
		Speed: u.Speed, InferenceGeo: u.InferenceGeo, At: ts,
	}
	if u.ServerToolUse != nil {
		usage.WebSearches = u.ServerToolUse.WebSearchRequests
	}
	if cc := u.CacheCreation; cc != nil && cc.Ephemeral5m+cc.Ephemeral1h == u.CacheCreationTokens {
		usage.CacheWrite5m, usage.CacheWrite1h = cc.Ephemeral5m, cc.Ephemeral1h
	}
	data := map[string]any{
		"model":              l.Message.Model,
		"message_id":         l.Message.ID,
		"input_tokens":       usage.Input,
		"output_tokens":      usage.Output,
		"cache_read_tokens":  usage.CacheRead,
		"cache_write_tokens": usage.CacheWrite5m + usage.CacheWrite1h,
	}
	if usage.CacheWrite1h > 0 {
		data["cache_write_1h_tokens"] = usage.CacheWrite1h
	}
	if usage.WebSearches > 0 {
		data["web_searches"] = usage.WebSearches
	}
	if usage.Speed == "fast" {
		data["speed"] = "fast"
	}
	if usage.InferenceGeo == "us" {
		data["inference_geo"] = "us"
	}
	if cost, ok := pricing.Default().Cost(l.Message.Model, usage); ok {
		data["cost_usd"] = cost
		data["cost_source"] = "computed"
	} else {
		data["cost_source"] = "unpriced"
	}

	sid := model.SessionID(Name, l.SessionID)
	e := model.Event{
		ID:         model.NewULID(ts),
		V:          model.SchemaVersion,
		TS:         ts.UTC(),
		ReceivedAt: meta.ReceivedAt.UTC(),
		Kind:       model.KindUsage,
		Agent:      model.Agent{Name: Name, Version: l.Version},
		Collector:  model.CollectorTranscript,
		User:       meta.User,
		SessionID:  sid,
		ActorID:    sid,
		Data:       data,
		DedupKey:   sid + ":usage:" + l.Message.ID,
	}
	if l.AgentID != "" {
		e.ActorID = sid + "/sub:" + l.AgentID
		e.ParentActor = sid
	}
	if l.CWD != "" {
		e.Project = &model.Project{CWD: l.CWD}
	}
	if meta.Ref != "" {
		e.Raw = &model.RawRef{Ref: meta.Ref}
	}
	return append(acts, e), nil
}

// costReport turns a cost-state line into a usage event marked as an agent
// report. Claude Code writes these periodically with the process's running
// total, which also covers calls that never appear as transcript lines
// (background model calls, web search fees). The engine attributes each
// increase of a process's total to the session the line was written in.
func costReport(l transcriptLine, meta adapters.TranscriptMeta) ([]model.Event, error) {
	if l.TotalCostUSD == nil || l.StartTime == 0 || l.SessionID == "" {
		return nil, nil
	}
	ts := meta.ReceivedAt
	if t, err := time.Parse(time.RFC3339Nano, l.Timestamp); err == nil {
		ts = t
	}
	process := fmt.Sprintf("%s:%d", Name, l.StartTime)
	total := *l.TotalCostUSD
	sid := model.SessionID(Name, l.SessionID)
	data := map[string]any{
		"report":         true,
		"cost_source":    "reported",
		"process":        process,
		"total_cost_usd": total,
	}
	if len(l.ModelUsage) > 0 {
		data["model_usage"] = l.ModelUsage
	}
	e := model.Event{
		ID: model.NewULID(ts), V: model.SchemaVersion, TS: ts.UTC(), ReceivedAt: meta.ReceivedAt.UTC(),
		Kind: model.KindUsage, Agent: model.Agent{Name: Name, Version: l.Version}, Collector: model.CollectorTranscript,
		User: meta.User, SessionID: sid, ActorID: sid, Data: data,
		DedupKey: fmt.Sprintf("%s:cost-state:%d:%.9f", sid, l.StartTime, total),
	}
	if meta.Ref != "" {
		e.Raw = &model.RawRef{Ref: meta.Ref}
	}
	return []model.Event{e}, nil
}

// titleUpdate turns an ai-title line (the title Claude Code generated for
// the session) into a session.update event. It takes precedence over a
// title derived from the first prompt.
func titleUpdate(l transcriptLine, meta adapters.TranscriptMeta) ([]model.Event, error) {
	title := strings.TrimSpace(l.AITitle)
	if title == "" || l.SessionID == "" {
		return nil, nil
	}
	sum := sha256.Sum256([]byte(title))
	sid := model.SessionID(Name, l.SessionID)
	ts := meta.ReceivedAt
	e := model.Event{
		ID: model.NewULID(ts), V: model.SchemaVersion, TS: ts.UTC(), ReceivedAt: ts.UTC(),
		Kind: model.KindSessionUpdate, Agent: model.Agent{Name: Name}, Collector: model.CollectorTranscript,
		User: meta.User, SessionID: sid, ActorID: sid,
		Data:     map[string]any{"title": title, "title_source": "agent"},
		DedupKey: fmt.Sprintf("%s:title:%x", sid, sum[:8]),
	}
	if meta.Ref != "" {
		e.Raw = &model.RawRef{Ref: meta.Ref}
	}
	return []model.Event{e}, nil
}
