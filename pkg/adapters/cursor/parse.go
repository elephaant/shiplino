// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

// Package cursor is the Cursor adapter (IDE agent and cursor-agent CLI).
//
// Hooks, checked against https://cursor.com/docs/agent/hooks on
// 2026-10-10: ~/.cursor/hooks.json, {"version": 1, "hooks": {event:
// [{command, timeout}]}}, JSON on stdin. Every payload carries
// conversation_id, generation_id, model, hook_event_name, cursor_version,
// workspace_roots and transcript_path.
//
// Only hooks that can't block or steer the agent are registered:
//
//	sessionStart / sessionEnd   session.start / session.end
//	beforeSubmitPrompt          turn.start (prompt); output is optional
//	stop                        turn.end
//	postToolUse                 tool.start + tool.end (+ shell.exec)
//	postToolUseFailure          tool.start + tool.end (failed)
//	afterFileEdit               file.edit
//	afterMCPExecution           mcp.call
//	afterAgentResponse          usage, when Cursor includes token counts
//	subagentStop                subagent.end
//	preCompact                  compact
//
// Permission hooks (preToolUse, beforeShellExecution, beforeMCPExecution,
// beforeReadFile, subagentStart) are never registered: Cursor treats
// missing or invalid output from them as a denial.
//
// Subagents run as their own conversations. Their transcript lives under
// the parent's: agent-transcripts/<parent>/subagents/<id>.jsonl, which is
// how their events are attached to the parent session.
package cursor

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/elephaant/shiplino/pkg/adapters"
	"github.com/elephaant/shiplino/pkg/model"
	"github.com/elephaant/shiplino/pkg/pricing"
)

// Name is the adapter name used in `shiplino hook --agent cursor`.
const Name = "cursor"

func init() { adapters.Register(Adapter{}) }

// Adapter is the Cursor adapter.
type Adapter struct{}

// Name implements adapters.Adapter.
func (Adapter) Name() string { return Name }

type payload struct {
	ConversationID string          `json:"conversation_id"`
	GenerationID   string          `json:"generation_id"`
	SessionID      string          `json:"session_id"`
	Model          string          `json:"model"`
	HookEventName  string          `json:"hook_event_name"`
	CursorVersion  string          `json:"cursor_version"`
	WorkspaceRoots []string        `json:"workspace_roots"`
	TranscriptPath string          `json:"transcript_path"`
	Prompt         string          `json:"prompt"`
	Status         string          `json:"status"`
	Reason         string          `json:"reason"`
	FinalStatus    string          `json:"final_status"`
	DurationMS     *int64          `json:"duration_ms"`
	Duration       *float64        `json:"duration"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	ToolOutput     json.RawMessage `json:"tool_output"`
	ToolUseID      string          `json:"tool_use_id"`
	CWD            string          `json:"cwd"`
	ErrorMessage   string          `json:"error_message"`
	FailureType    string          `json:"failure_type"`
	IsInterrupt    bool            `json:"is_interrupt"`
	FilePath       string          `json:"file_path"`
	Edits          []struct {
		Old string `json:"old_string"`
		New string `json:"new_string"`
	} `json:"edits"`
	MCPServer    string `json:"mcp_server_name"`
	SubagentType string `json:"subagent_type"`
	AgentPath    string `json:"agent_transcript_path"`
	Summary      string `json:"summary"`
	ToolCalls    *int   `json:"tool_call_count"`
	Trigger      string `json:"trigger"`
	ComposerMode string `json:"composer_mode"`
	Background   bool   `json:"is_background_agent"`
}

// ParseHook implements adapters.Adapter.
func (Adapter) ParseHook(raw []byte, meta adapters.HookMeta) ([]model.Event, error) {
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("cursor: decode hook payload: %w", err)
	}
	conv := first(p.ConversationID, p.SessionID)
	if conv == "" {
		return nil, fmt.Errorf("cursor: hook payload has no conversation_id")
	}
	event := first(p.HookEventName, meta.Event)
	b := newBuilder(&p, meta, conv)

	switch event {
	case "sessionStart":
		return b.one(model.KindSessionStart, compact(map[string]any{
			"model": p.Model, "transcript_path": p.TranscriptPath, "mode": p.ComposerMode, "background": p.Background,
		})), nil
	case "sessionEnd":
		status := "ended"
		if p.Reason == "error" || p.FinalStatus == "error" {
			status = "failed"
		}
		return b.one(model.KindSessionEnd, compact(map[string]any{"reason": p.Reason, "status": status, "duration_ms": ms(p.DurationMS, nil), "error": p.ErrorMessage})), nil
	case "beforeSubmitPrompt":
		e := b.base(model.KindTurnStart, compact(map[string]any{
			"prompt": p.Prompt, "prompt_chars": len([]rune(p.Prompt)), "model": p.Model, "transcript_path": p.TranscriptPath,
		}))
		b.keyed(&e, "turn:"+p.GenerationID, p.GenerationID)
		return []model.Event{e}, nil
	case "stop":
		status := "ok"
		switch p.Status {
		case "aborted":
			status = "interrupted"
		case "error":
			status = "error"
		}
		e := b.base(model.KindTurnEnd, map[string]any{"status": status})
		b.keyed(&e, "turnend:"+p.GenerationID, p.GenerationID)
		return []model.Event{e}, nil
	case "postToolUse", "postToolUseFailure":
		return b.tool(event == "postToolUse"), nil
	case "afterFileEdit":
		if p.FilePath == "" {
			return nil, nil
		}
		added, removed := 0, 0
		pairs := make([][2]string, 0, len(p.Edits))
		for _, ed := range p.Edits {
			a, r := lineDiff(ed.Old, ed.New)
			added, removed = added+a, removed+r
			pairs = append(pairs, [2]string{ed.Old, ed.New})
		}
		d := map[string]any{"path": joinPath(b.cwd, p.FilePath), "op": "modify"}
		if len(p.Edits) > 0 {
			d["lines_added"], d["lines_removed"], d["lines_source"] = added, removed, "computed"
		}
		if patch := adapters.SnippetPatch(pairs...); patch != "" {
			d["patch"], d["patch_source"] = patch, "computed"
		}
		return b.one(model.KindFileEdit, d), nil
	case "afterMCPExecution":
		return b.one(model.KindMCPCall, compact(map[string]any{
			"server": p.MCPServer, "tool": p.ToolName, "ok": true, "duration_ms": ms(nil, p.Duration),
		})), nil
	case "afterAgentResponse":
		return b.usage(raw), nil
	case "subagentStop":
		id := strings.TrimSuffix(path.Base(strings.ReplaceAll(p.AgentPath, `\`, "/")), ".jsonl")
		if id == "" || id == "." {
			return nil, nil
		}
		e := b.base(model.KindSubagentEnd, compact(map[string]any{
			"child_session_id": b.sid + "/sub:" + id, "agent_type": p.SubagentType, "status": first(p.Status, "completed"),
			"transcript_path": p.AgentPath, "duration_ms": ms(p.DurationMS, nil), "summary": p.Summary,
		}))
		if p.ToolCalls != nil {
			e.Data["tool_calls"] = *p.ToolCalls
		}
		return []model.Event{e}, nil
	case "preCompact":
		return b.one(model.KindCompact, map[string]any{"phase": "pre", "trigger": p.Trigger}), nil
	}
	return nil, fmt.Errorf("%w: cursor %q", adapters.ErrUnknownEvent, event)
}

type builder struct {
	p        *payload
	meta     adapters.HookMeta
	sid      string // root session
	actor    string
	cwd      string
	subagent bool
}

func newBuilder(p *payload, meta adapters.HookMeta, conv string) builder {
	b := builder{p: p, meta: meta, sid: model.SessionID(Name, conv)}
	b.actor = b.sid
	if parent, sub, ok := subagentPath(p.TranscriptPath); ok {
		b.sid = model.SessionID(Name, parent)
		b.actor = b.sid + "/sub:" + sub
		b.subagent = true
	}
	b.cwd = p.CWD
	if b.cwd == "" && len(p.WorkspaceRoots) > 0 {
		b.cwd = p.WorkspaceRoots[0]
	}
	return b
}

func (b builder) base(kind model.Kind, data map[string]any) model.Event {
	e := model.Event{
		ID: model.NewULID(b.meta.ReceivedAt), V: model.SchemaVersion, TS: b.meta.ReceivedAt.UTC(), ReceivedAt: b.meta.ReceivedAt.UTC(),
		Kind: kind, Agent: model.Agent{Name: Name, Version: b.p.CursorVersion}, Collector: model.CollectorHook,
		MachineID: b.meta.MachineID, User: b.meta.User, SessionID: b.sid, ActorID: b.actor, TurnID: b.p.GenerationID, Data: data,
		DedupKey: b.actor + ":" + string(kind) + ":" + b.meta.EnvelopeID,
	}
	if b.subagent {
		e.ParentActor, e.ActorType = b.sid, "subagent"
	}
	if b.cwd != "" {
		e.Project = &model.Project{CWD: b.cwd}
	}
	if b.meta.Ref != "" {
		e.Raw = &model.RawRef{Ref: b.meta.Ref}
	}
	return e
}

// keyed gives e a stable dedup key when the agent supplied an id.
func (b builder) keyed(e *model.Event, key, id string) {
	if id != "" {
		e.DedupKey = b.actor + ":" + key
	}
}

func (b builder) one(kind model.Kind, data map[string]any) []model.Event {
	return []model.Event{b.base(kind, data)}
}

// tool turns a finished tool call into a start/end pair. Cursor reports
// the duration, so the start is placed that long before the end.
func (b builder) tool(ok bool) []model.Event {
	p := b.p
	tool := NormalizeTool(p.ToolName)
	dur := ms(nil, p.Duration)
	start := b.base(model.KindToolStart, map[string]any{
		"tool_call_id": p.ToolUseID, "tool": tool, "tool_raw": p.ToolName, "input_summary": summarize(p.ToolName, p.ToolInput),
	})
	if dur != nil {
		start.TS = start.TS.Add(-time.Duration(*dur) * time.Millisecond)
	}
	end := b.base(model.KindToolEnd, compact(map[string]any{"tool_call_id": p.ToolUseID, "tool": tool, "ok": ok, "duration_ms": dur}))
	if !ok {
		end.Data["error"] = first(p.ErrorMessage, p.FailureType)
		if p.IsInterrupt {
			end.Data["interrupted"] = true
		}
	}
	b.keyed(&start, p.ToolUseID+":start", p.ToolUseID)
	b.keyed(&end, p.ToolUseID+":end", p.ToolUseID)
	out := []model.Event{start, end}
	if tool == model.ToolShell {
		var in struct {
			Command string `json:"command"`
			Dir     string `json:"working_directory"`
		}
		_ = json.Unmarshal(p.ToolInput, &in)
		d := compact(map[string]any{"command": in.Command, "tool_call_id": p.ToolUseID, "cwd": first(in.Dir, b.cwd), "duration_ms": dur})
		if code, found := exitCode(p.ToolOutput); found {
			d["exit_code"] = code
		} else if !ok {
			d["exit_code"] = 1
		}
		sh := b.base(model.KindShellExec, d)
		b.keyed(&sh, p.ToolUseID+":shell", p.ToolUseID)
		out = append(out, sh)
	}
	return out
}

// usage reads token counts from afterAgentResponse. Cursor sends them in
// interactive sessions; the field names aren't documented, so the common
// spellings are accepted, at the top level or under "usage"/"tokens".
func (b builder) usage(raw []byte) []model.Event {
	var top map[string]json.RawMessage
	if json.Unmarshal(raw, &top) != nil {
		return nil
	}
	in, out, cr, cw, found := tokens(top)
	for _, k := range []string{"usage", "token_usage", "tokens", "token_count", "tokenCount"} {
		if found {
			break
		}
		var nested map[string]json.RawMessage
		if json.Unmarshal(top[k], &nested) == nil {
			in, out, cr, cw, found = tokens(nested)
		}
	}
	if !found {
		return nil
	}
	data := map[string]any{"model": b.p.Model, "message_id": b.p.GenerationID, "input_tokens": in, "output_tokens": out,
		"cache_read_tokens": cr, "cache_write_tokens": cw}
	if cost, ok := pricing.Default().Cost(b.p.Model, pricing.Usage{Input: in, Output: out, CacheRead: cr, CacheWrite5m: cw, At: b.meta.ReceivedAt}); ok {
		data["cost_usd"], data["cost_source"] = cost, "computed"
	} else {
		data["cost_source"] = "unpriced"
	}
	e := b.base(model.KindUsage, data)
	b.keyed(&e, "usage:"+b.p.GenerationID, b.p.GenerationID)
	return []model.Event{e}
}

func tokens(m map[string]json.RawMessage) (in, out, cacheRead, cacheWrite int64, found bool) {
	get := func(keys ...string) int64 {
		for _, k := range keys {
			var n int64
			if json.Unmarshal(m[k], &n) == nil && m[k] != nil {
				found = true
				return n
			}
		}
		return 0
	}
	in = get("input_tokens", "inputTokens", "prompt_tokens")
	out = get("output_tokens", "outputTokens", "completion_tokens")
	cacheRead = get("cache_read_tokens", "cacheReadTokens", "cache_read_input_tokens", "cached_input_tokens")
	cacheWrite = get("cache_write_tokens", "cacheWriteTokens", "cache_creation_input_tokens")
	return
}

// exitCode looks for an exit code in a Shell tool's output (a JSON string
// or object).
func exitCode(raw json.RawMessage) (int, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		raw = json.RawMessage(s)
	}
	var o struct {
		ExitCode  *int `json:"exitCode"`
		ExitCode2 *int `json:"exit_code"`
	}
	if json.Unmarshal(raw, &o) != nil {
		return 0, false
	}
	if o.ExitCode != nil {
		return *o.ExitCode, true
	}
	if o.ExitCode2 != nil {
		return *o.ExitCode2, true
	}
	return 0, false
}

// NormalizeTool maps Cursor tool names to normalized tool names.
func NormalizeTool(name string) string {
	switch {
	case name == "Shell" || name == "AwaitShell" || name == "Await":
		return model.ToolShell
	case name == "Read" || name == "ReadLints":
		return model.ToolRead
	case name == "Write":
		return model.ToolWrite
	case name == "StrReplace" || name == "Edit" || name == "MultiEdit" || name == "ApplyPatch" || name == "Delete":
		return model.ToolEdit
	case name == "Grep" || name == "Glob" || name == "SemanticSearch" || name == "SearchConversations":
		return model.ToolSearch
	case name == "WebSearch" || name == "WebFetch":
		return model.ToolWeb
	case name == "Task" || name == "Subagent":
		return model.ToolTask
	case strings.HasPrefix(name, "MCP:") || name == "CallMcpTool" || name == "CallDynamicTool":
		return model.ToolMCP
	}
	return model.ToolOther
}

// summarize is a short description of a tool call for the board.
func summarize(tool string, raw json.RawMessage) string {
	var in map[string]any
	if json.Unmarshal(raw, &in) != nil {
		var s string
		if json.Unmarshal(raw, &s) == nil && json.Unmarshal([]byte(s), &in) != nil {
			return ""
		}
	}
	for _, k := range []string{"command", "path", "file_path", "pattern", "glob_pattern", "search_term", "url", "description", "query"} {
		if v, ok := in[k].(string); ok && v != "" {
			if len(v) > 200 {
				v = v[:200]
			}
			return v
		}
	}
	return strings.TrimPrefix(tool, "MCP:")
}

// lineDiff counts changed lines between two snippets, ignoring the lines
// they share at the start and end.
func lineDiff(old, new string) (added, removed int) {
	a, b := splitLines(old), splitLines(new)
	for len(a) > 0 && len(b) > 0 && a[0] == b[0] {
		a, b = a[1:], b[1:]
	}
	for len(a) > 0 && len(b) > 0 && a[len(a)-1] == b[len(b)-1] {
		a, b = a[:len(a)-1], b[:len(b)-1]
	}
	return len(b), len(a)
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// ms returns a duration in milliseconds from whichever field is set.
func ms(i *int64, f *float64) *int64 {
	if i != nil {
		return i
	}
	if f != nil {
		v := int64(*f)
		return &v
	}
	return nil
}

// joinPath resolves p against cwd in the agent's own path style.
func joinPath(cwd, p string) string {
	if cwd == "" || p == "" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\\`) || (len(p) > 2 && p[1] == ':' && (p[2] == '\\' || p[2] == '/')) {
		return p
	}
	if strings.HasPrefix(cwd, "/") {
		return path.Join(cwd, p)
	}
	return strings.TrimRight(cwd, `\/`) + `\` + strings.ReplaceAll(p, "/", `\`)
}

func compact(m map[string]any) map[string]any {
	for k, v := range m {
		switch x := v.(type) {
		case string:
			if x == "" {
				delete(m, k)
			}
		case bool:
			if !x {
				delete(m, k)
			}
		case *int64:
			if x == nil {
				delete(m, k)
			} else {
				m[k] = *x
			}
		case nil:
			delete(m, k)
		}
	}
	return m
}

func first(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
