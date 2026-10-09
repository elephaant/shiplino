// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package claudecode

import (
	"encoding/json"
	"fmt"
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
		} `json:"usage"`
	} `json:"message"`
}

// ParseTranscriptLine implements adapters.TranscriptParser.
func (Adapter) ParseTranscriptLine(line []byte, meta adapters.TranscriptMeta) ([]model.Event, error) {
	var l transcriptLine
	if err := json.Unmarshal(line, &l); err != nil {
		return nil, fmt.Errorf("claude-code transcript: %w", err)
	}
	u := l.Message.Usage
	if l.Type != "assistant" || u == nil || l.Message.ID == "" || l.SessionID == "" {
		return nil, nil
	}
	if l.Message.Model == "<synthetic>" { // local error messages, not API calls
		return nil, nil
	}

	usage := pricing.Usage{Input: u.InputTokens, Output: u.OutputTokens, CacheRead: u.CacheReadTokens, CacheWrite5m: u.CacheCreationTokens}
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
	if cost, ok := pricing.Default.Cost(l.Message.Model, usage); ok {
		data["cost_usd"] = cost
		data["cost_source"] = "computed"
	} else {
		data["cost_source"] = "unpriced"
	}

	ts := meta.ReceivedAt
	if t, err := time.Parse(time.RFC3339Nano, l.Timestamp); err == nil {
		ts = t
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
	return []model.Event{e}, nil
}
