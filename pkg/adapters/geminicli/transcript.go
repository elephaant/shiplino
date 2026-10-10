// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package geminicli

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/elephaant/shiplino/pkg/adapters"
	"github.com/elephaant/shiplino/pkg/model"
	"github.com/elephaant/shiplino/pkg/pricing"
)

// Chat recordings, checked against packages/core/src/services/
// chatRecordingService.ts and chatRecordingTypes.ts of v0.63.0 on
// 2026-10-10: ~/.gemini/tmp/<project>/chats/session-<time>-<id>.jsonl,
// subagents in chats/<parent session id>/<id>.jsonl. The hook payload's
// transcript_path points at the file. <project> is a short id; the
// project's root path is in ~/.gemini/tmp/<project>/.project_root.
// Append-only JSONL:
//
//	{sessionId, projectHash, startTime, kind?}  first line (kind: main | subagent)
//	{id, timestamp, type, content, …}           a message; re-appended whole
//	                                            whenever it changes
//	{"$set": {…}}                               metadata update (summary, …)
//	{"$rewindTo": id}                           the user rewound
//
// Message types: user, gemini, info, error, warning. A gemini message has
// model, tokens {input, output, cached, thoughts, tool, total} (one API
// response; the Gemini API counts cached tokens inside input) and
// toolCalls [{id, name, args, result, status, timestamp, resultDisplay}].
// Older releases wrote one .json document per session; those aren't read.
//
// Tokens and Gemini's own session summary (the title) are always used.
// Prompts and tool calls rebuild sessions the hooks didn't see; for
// sessions with hooks the engine drops them as redundant (the hooks have
// no tool call ids to match on).

type tokens struct {
	Input    int64 `json:"input"`
	Output   int64 `json:"output"`
	Cached   int64 `json:"cached"`
	Thoughts int64 `json:"thoughts"`
	Tool     int64 `json:"tool"`
}

type toolCallRecord struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Args          json.RawMessage `json:"args"`
	Result        json.RawMessage `json:"result"`
	Status        string          `json:"status"`
	Timestamp     string          `json:"timestamp"`
	ResultDisplay json.RawMessage `json:"resultDisplay"`
}

type record struct {
	SessionID string                     `json:"sessionId"`
	StartTime string                     `json:"startTime"`
	Kind      string                     `json:"kind"`
	Summary   string                     `json:"summary"`
	ID        string                     `json:"id"`
	Timestamp string                     `json:"timestamp"`
	Type      string                     `json:"type"`
	Content   json.RawMessage            `json:"content"`
	Model     string                     `json:"model"`
	Tokens    *tokens                    `json:"tokens"`
	ToolCalls []toolCallRecord           `json:"toolCalls"`
	Set       map[string]json.RawMessage `json:"$set"`
}

// ParseTranscriptLine implements adapters.TranscriptParser. meta.State
// keeps the session id and kind; the parent session and project root are
// resolved from the file's path (meta.Ref) on the first event.
func (Adapter) ParseTranscriptLine(raw []byte, meta adapters.TranscriptMeta) ([]model.Event, error) {
	var r record
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("gemini-cli chat: %w", err)
	}
	st := meta.State
	if st == nil {
		st = map[string]string{}
	}
	if r.ID == "" && r.Set == nil && r.SessionID != "" { // first line
		st["session"], st["kind"] = r.SessionID, r.Kind
	}
	if meta.Warmup || st["session"] == "" {
		return nil, nil
	}
	b := newChatBuilder(st, meta)
	if b == nil {
		return nil, nil
	}

	switch {
	case r.Set != nil:
		var summary string
		if json.Unmarshal(r.Set["summary"], &summary) != nil || summary == "" || b.sub {
			return nil, nil
		}
		h := fnv.New64a()
		h.Write([]byte(summary))
		return []model.Event{b.event(model.KindSessionUpdate, "", map[string]any{"title": summary}, fmt.Sprintf("title:%x", h.Sum64()))}, nil
	case r.ID == "":
		if b.sub || r.SessionID == "" { // $rewindTo, or a subagent's first line
			return nil, nil
		}
		return []model.Event{b.event(model.KindSessionStart, r.StartTime, compact(map[string]any{"source": "transcript", "title": r.Summary}), "session.start")}, nil
	case r.Type == "user":
		prompt := text(r.Content)
		if prompt == "" {
			return nil, nil
		}
		return []model.Event{b.event(model.KindTurnStart, r.Timestamp, map[string]any{"prompt": prompt, "prompt_chars": len([]rune(prompt))}, "turn:"+r.ID)}, nil
	case r.Type == "gemini":
		var out []model.Event
		if t := r.Tokens; t != nil {
			out = append(out, b.usage(r, t))
		}
		for _, tc := range r.ToolCalls {
			out = append(out, b.tool(tc, r.Timestamp)...)
		}
		return out, nil
	}
	return nil, nil
}

type chatBuilder struct {
	st   map[string]string
	meta adapters.TranscriptMeta
	sid  string // the root session
	sub  bool   // a subagent's file
}

// newChatBuilder resolves the session (a subagent's parent comes from its
// folder name) and project root once per file, from meta.Ref.
func newChatBuilder(st map[string]string, meta adapters.TranscriptMeta) *chatBuilder {
	file := strings.TrimPrefix(meta.Ref, "transcript:")
	if i := strings.LastIndexByte(file, '#'); i >= 0 {
		file = file[:i]
	}
	if st["resolved"] == "" && file != "" {
		st["resolved"] = "1"
		dir := filepath.Dir(file)
		if st["kind"] == "subagent" {
			st["parent"] = filepath.Base(dir)
			dir = filepath.Dir(dir)
		}
		if filepath.Base(dir) == "chats" {
			if b, err := os.ReadFile(filepath.Join(filepath.Dir(dir), ".project_root")); err == nil {
				st["cwd"] = strings.TrimSpace(string(b))
			}
		}
	}
	b := &chatBuilder{st: st, meta: meta, sid: model.SessionID(Name, st["session"]), sub: st["kind"] == "subagent"}
	if b.sub {
		if st["parent"] == "" {
			return nil // can't place it without its parent
		}
		b.sid = model.SessionID(Name, st["parent"])
	}
	return b
}

// at is the time a record gives, or when it was read.
func (b *chatBuilder) at(ts string) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
		return t
	}
	return b.meta.ReceivedAt
}

func (b *chatBuilder) event(kind model.Kind, ts string, data map[string]any, key string) model.Event {
	t := b.at(ts)
	e := model.Event{
		ID: model.NewULID(t), V: model.SchemaVersion, TS: t.UTC(), ReceivedAt: b.meta.ReceivedAt.UTC(),
		Kind: kind, Agent: model.Agent{Name: Name}, Collector: model.CollectorTranscript,
		User: b.meta.User, SessionID: b.sid, ActorID: b.sid, Data: data,
	}
	if b.sub {
		e.ActorID, e.ParentActor, e.ActorType = b.sid+"/sub:"+b.st["session"], b.sid, "subagent"
	}
	e.DedupKey = e.ActorID + ":" + key
	if b.st["cwd"] != "" {
		e.Project = &model.Project{CWD: b.st["cwd"]}
	}
	if b.meta.Ref != "" {
		e.Raw = &model.RawRef{Ref: b.meta.Ref}
	}
	return e
}

// usage prices one API response. Thinking and tool-use prompt tokens are
// billed as output and input.
func (b *chatBuilder) usage(r record, t *tokens) model.Event {
	uncached := max(t.Input-t.Cached, 0) + t.Tool
	output := t.Output + t.Thoughts
	data := map[string]any{
		"model": r.Model, "message_id": r.ID, "input_tokens": uncached, "output_tokens": output,
		"cache_read_tokens": t.Cached, "cache_write_tokens": int64(0), "reasoning_tokens": t.Thoughts,
	}
	if cost, ok := pricing.Default().Cost(r.Model, pricing.Usage{Input: uncached, Output: output, CacheRead: t.Cached, At: b.at(r.Timestamp)}); ok {
		data["cost_usd"], data["cost_source"] = cost, "computed"
	} else {
		data["cost_source"] = "unpriced"
	}
	return b.event(model.KindUsage, r.Timestamp, data, "usage:"+r.ID)
}

// tool turns a finished tool call record into events.
func (b *chatBuilder) tool(tc toolCallRecord, msgTS string) []model.Event {
	switch tc.Status {
	case "success", "error", "cancelled":
	default:
		return nil // still running; the record is appended again when it ends
	}
	if tc.ID == "" {
		return nil
	}
	call := toolCall{ID: tc.ID, Name: tc.Name, Args: tc.Args, OK: tc.Status == "success", Output: tc.Result, Display: tc.ResultDisplay}
	ds := call.derive(b.st["cwd"])
	ts := firstOf(tc.Timestamp, msgTS)
	key := "tool:" + tc.ID
	tool := NormalizeTool(tc.Name)
	out := []model.Event{
		b.event(model.KindToolStart, ts, map[string]any{"tool_call_id": tc.ID, "tool": tool, "tool_raw": tc.Name, "input_summary": summarize(tc.Name, tc.Args)}, key+":start"),
		b.event(model.KindToolEnd, ts, map[string]any{"tool_call_id": tc.ID, "tool": tool, "ok": call.ok(ds)}, key+":end"),
	}
	for _, d := range ds {
		out = append(out, b.event(d.kind, ts, d.data, key+":"+d.suffix))
	}
	return out
}

// text joins the text parts of a message's content (a string or parts).
func text(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(raw, &parts)
	var out []string
	for _, p := range parts {
		if p.Text != "" {
			out = append(out, p.Text)
		}
	}
	return strings.Join(out, "\n")
}

// TranscriptRoots implements adapters.TranscriptDiscoverer: every
// project's chats, including subagents'.
func (Adapter) TranscriptRoots(userHome string, _, _ time.Time) []string {
	base := filepath.Join(configDir(userHome), "tmp")
	if _, err := os.Stat(base); err != nil {
		return nil
	}
	return []string{
		filepath.Join(base, "*", "chats", "session-*.jsonl"),
		filepath.Join(base, "*", "chats", "*", "*.jsonl"),
	}
}
