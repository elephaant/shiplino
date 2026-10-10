// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package codex

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/elephaant/shiplino/pkg/adapters"
	"github.com/elephaant/shiplino/pkg/model"
)

// Name is the agent name used in events and spool paths.
const Name = "codex"

// Hook payloads checked against the official Codex hooks reference
// (developers.openai.com/codex/hooks) on 2026-10-10. Common fields:
// session_id, transcript_path, cwd, hook_event_name, model; turn_id on
// turn-scoped events.
//
//	SessionStart        session.start
//	SessionEnd          session.end
//	UserPromptSubmit    turn.start
//	Stop                turn.end (ok)
//	Interrupt           turn.end (interrupted)
//	PreToolUse          tool.start
//	PostToolUse         tool.end + shell.exec / file.edit (apply_patch)
//	PermissionRequest   waiting.start
//	SubagentStart/Stop  subagent.start / subagent.end
//	PreCompact/PostCompact compact

func init() { adapters.Register(Adapter{}) }

// Adapter is the Codex adapter.
type Adapter struct{}

// Name implements adapters.Adapter.
func (Adapter) Name() string { return Name }

type payload struct {
	CursorVersion   string          `json:"cursor_version"`
	SessionID       string          `json:"session_id"`
	TurnID          string          `json:"turn_id"`
	TranscriptPath  string          `json:"transcript_path"`
	CWD             string          `json:"cwd"`
	HookEventName   string          `json:"hook_event_name"`
	Model           string          `json:"model"`
	PermissionMode  string          `json:"permission_mode"`
	Source          string          `json:"source"`
	Reason          string          `json:"reason"`
	Prompt          string          `json:"prompt"`
	ToolName        string          `json:"tool_name"`
	ToolUseID       string          `json:"tool_use_id"`
	ToolInput       json.RawMessage `json:"tool_input"`
	ToolResponse    json.RawMessage `json:"tool_response"`
	AgentID         string          `json:"agent_id"`
	AgentType       string          `json:"agent_type"`
	AgentTranscript string          `json:"agent_transcript_path"`
	LastMessage     string          `json:"last_assistant_message"`
	Trigger         string          `json:"trigger"`
}

// ParseHook implements adapters.Adapter.
func (Adapter) ParseHook(raw []byte, meta adapters.HookMeta) ([]model.Event, error) {
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("codex: decode hook payload: %w", err)
	}
	// Cursor also runs Claude Code and Codex hooks, with its own payloads.
	// Cursor's native hooks record those sessions, so skip them here.
	if p.CursorVersion != "" {
		return nil, nil
	}
	if p.SessionID == "" {
		return nil, fmt.Errorf("codex: hook payload has no session_id")
	}
	event := p.HookEventName
	if event == "" {
		event = meta.Event
	}
	b := builder{p: &p, meta: meta, sid: model.SessionID(Name, p.SessionID)}

	switch event {
	case "SessionStart":
		return b.one(model.KindSessionStart, compact(map[string]any{
			"source": p.Source, "model": p.Model, "permission_mode": p.PermissionMode, "transcript_path": p.TranscriptPath,
		})), nil
	case "SessionEnd":
		return b.one(model.KindSessionEnd, map[string]any{"reason": p.Reason, "status": "ended"}), nil
	case "UserPromptSubmit":
		e := b.base(model.KindTurnStart, compact(map[string]any{
			"prompt": p.Prompt, "prompt_chars": len([]rune(p.Prompt)), "transcript_path": p.TranscriptPath, "model": p.Model,
		}))
		if p.TurnID != "" {
			e.DedupKey = b.sid + ":turn:" + p.TurnID // same key as the rollout's task_started
		}
		return []model.Event{e}, nil
	case "Stop":
		e := b.base(model.KindTurnEnd, compact(map[string]any{"status": "ok", "assistant_summary": p.LastMessage}))
		if p.TurnID != "" {
			e.DedupKey = b.sid + ":turnend:" + p.TurnID
		}
		return []model.Event{e}, nil
	case "Interrupt":
		e := b.base(model.KindTurnEnd, map[string]any{"status": "interrupted"})
		if p.TurnID != "" {
			e.DedupKey = b.sid + ":turnend:" + p.TurnID
		}
		return []model.Event{e}, nil
	case "PreToolUse":
		e := b.base(model.KindToolStart, map[string]any{
			"tool_call_id": p.ToolUseID, "tool": NormalizeTool(p.ToolName), "tool_raw": p.ToolName, "input_summary": summarize(p.ToolName, p.ToolInput),
		})
		if p.ToolUseID != "" {
			e.DedupKey = b.sid + ":" + p.ToolUseID + ":start"
		}
		return []model.Event{e}, nil
	case "PostToolUse":
		return b.toolEnd(), nil
	case "PermissionRequest":
		return b.one(model.KindWaitingStart, map[string]any{
			"reason": "permission", "message": "Approve: " + summarize(p.ToolName, p.ToolInput), "tool_raw": p.ToolName,
		}), nil
	case "SubagentStart":
		return b.subagent(model.KindSubagentStart, compact(map[string]any{
			"child_session_id": b.sid + "/sub:" + p.AgentID, "agent_type": p.AgentType,
		})), nil
	case "SubagentStop":
		return b.subagent(model.KindSubagentEnd, compact(map[string]any{
			"child_session_id": b.sid + "/sub:" + p.AgentID, "agent_type": p.AgentType, "status": "done", "transcript_path": p.AgentTranscript,
		})), nil
	case "PreCompact":
		return b.one(model.KindCompact, map[string]any{"phase": "pre", "trigger": p.Trigger}), nil
	case "PostCompact":
		return b.one(model.KindCompact, map[string]any{"phase": "post", "trigger": p.Trigger}), nil
	}
	return nil, fmt.Errorf("codex %q: %w", event, adapters.ErrUnknownEvent)
}

type builder struct {
	p    *payload
	meta adapters.HookMeta
	sid  string
}

func (b builder) base(kind model.Kind, data map[string]any) model.Event {
	e := model.Event{
		ID: model.NewULID(b.meta.ReceivedAt), V: model.SchemaVersion, TS: b.meta.ReceivedAt.UTC(), ReceivedAt: b.meta.ReceivedAt.UTC(),
		Kind: kind, Agent: model.Agent{Name: Name}, Collector: model.CollectorHook,
		MachineID: b.meta.MachineID, User: b.meta.User, SessionID: b.sid, ActorID: b.sid, TurnID: b.p.TurnID, Data: data,
		DedupKey: b.sid + ":" + string(kind) + ":" + b.meta.EnvelopeID,
	}
	if b.p.AgentID != "" && kind != model.KindSubagentStart && kind != model.KindSubagentEnd {
		e.ActorID, e.ParentActor, e.ActorType = b.sid+"/sub:"+b.p.AgentID, b.sid, b.p.AgentType
	}
	if b.p.CWD != "" {
		e.Project = &model.Project{CWD: b.p.CWD}
	}
	if b.meta.Ref != "" {
		e.Raw = &model.RawRef{Ref: b.meta.Ref}
	}
	return e
}

func (b builder) one(kind model.Kind, data map[string]any) []model.Event {
	return []model.Event{b.base(kind, data)}
}

func (b builder) subagent(kind model.Kind, data map[string]any) []model.Event {
	return []model.Event{b.base(kind, data)}
}

func (b builder) toolEnd() []model.Event {
	tool := NormalizeTool(b.p.ToolName)
	end := b.base(model.KindToolEnd, map[string]any{"tool_call_id": b.p.ToolUseID, "tool": tool, "ok": true})
	var resp struct {
		ExitCode *int  `json:"exit_code"`
		Success  *bool `json:"success"`
	}
	_ = json.Unmarshal(b.p.ToolResponse, &resp)
	if resp.Success != nil && !*resp.Success || resp.ExitCode != nil && *resp.ExitCode != 0 {
		end.Data["ok"] = false
	}
	if b.p.ToolUseID != "" {
		end.DedupKey = b.sid + ":" + b.p.ToolUseID + ":end"
	}
	out := []model.Event{end}
	derived := func(kind model.Kind, suffix string, d map[string]any) {
		e := b.base(kind, d)
		if b.p.ToolUseID != "" {
			e.DedupKey = b.sid + ":" + b.p.ToolUseID + ":" + suffix
		}
		out = append(out, e)
	}
	switch tool {
	case model.ToolShell:
		d := map[string]any{"command": commandText(b.p.ToolInput), "tool_call_id": b.p.ToolUseID}
		if resp.ExitCode != nil {
			d["exit_code"] = *resp.ExitCode
		}
		if b.p.CWD != "" {
			d["cwd"] = b.p.CWD
		}
		derived(model.KindShellExec, "shell", d)
	case model.ToolEdit:
		for i, fe := range patchFiles(patchText(b.p.ToolInput), b.p.CWD) {
			derived(model.KindFileEdit, fmt.Sprintf("file%d", i), fe)
		}
	}
	return out
}

// NormalizeTool maps Codex tool names to normalized tool names.
func NormalizeTool(name string) string {
	switch name {
	case "shell", "exec", "exec_command", "local_shell", "unified_exec", "container.exec", "write_stdin":
		return model.ToolShell
	case "apply_patch":
		return model.ToolEdit
	case "view_image", "read_file":
		return model.ToolRead
	case "web_search", "web_fetch":
		return model.ToolWeb
	case "spawn_agent", "agent":
		return model.ToolTask
	}
	if strings.Contains(name, "__") || strings.HasPrefix(name, "mcp") {
		return model.ToolMCP
	}
	return model.ToolOther
}

// commandText reads a shell command given as a string or an argv array
// (["bash", "-lc", "…"] shows just the script).
func commandText(raw json.RawMessage) string {
	var in struct {
		Command json.RawMessage `json:"command"`
		Cmd     string          `json:"cmd"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return ""
	}
	if in.Cmd != "" {
		return in.Cmd
	}
	var s string
	if json.Unmarshal(in.Command, &s) == nil {
		return s
	}
	var argv []string
	if json.Unmarshal(in.Command, &argv) == nil {
		if len(argv) >= 3 && (argv[1] == "-lc" || argv[1] == "-c") {
			return argv[2]
		}
		return strings.Join(argv, " ")
	}
	return ""
}

func patchText(raw json.RawMessage) string {
	// The hooks docs (checked 2026-10-10) put the patch in "command";
	// older payloads used "input" or "patch".
	var in struct {
		Command json.RawMessage `json:"command"`
		Input   string          `json:"input"`
		Patch   string          `json:"patch"`
	}
	if json.Unmarshal(raw, &in) == nil {
		var cmd string
		_ = json.Unmarshal(in.Command, &cmd)
		if strings.Contains(cmd, "*** Begin Patch") {
			return cmd
		}
		if in.Input != "" {
			return in.Input
		}
		return in.Patch
	}
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

// patchFiles reads Codex's apply_patch format ("*** Add File: p",
// "*** Update File: p", "*** Delete File: p"; +/- lines) into file edits.
// Each file's hunks are kept as its patch (the agent's own diff).
func patchFiles(patch, cwd string) []map[string]any {
	var out []map[string]any
	var cur map[string]any
	var body []string
	flush := func() {
		if cur != nil && len(body) > 0 {
			if !strings.HasPrefix(body[0], "@@") {
				body = append([]string{"@@ @@"}, body...)
			}
			cur["patch"], cur["patch_source"] = strings.Join(body, "\n")+"\n", "agent"
		}
		body = nil
	}
	for _, line := range strings.Split(patch, "\n") {
		for _, h := range [][2]string{{"*** Add File: ", "create"}, {"*** Update File: ", "modify"}, {"*** Delete File: ", "delete"}} {
			if strings.HasPrefix(line, h[0]) {
				flush()
				path := strings.TrimSpace(strings.TrimPrefix(line, h[0]))
				if cwd != "" {
					path = joinPath(cwd, path)
				}
				cur = map[string]any{"path": path, "op": h[1], "lines_added": 0, "lines_removed": 0, "lines_source": "agent"}
				out = append(out, cur)
			}
		}
		if cur == nil || strings.HasPrefix(line, "***") {
			continue
		}
		switch {
		case strings.HasPrefix(line, "+"):
			cur["lines_added"] = cur["lines_added"].(int) + 1
		case strings.HasPrefix(line, "-"):
			cur["lines_removed"] = cur["lines_removed"].(int) + 1
		case strings.HasPrefix(line, " "), strings.HasPrefix(line, "@@"):
		default:
			continue // not part of a hunk
		}
		body = append(body, line)
	}
	flush()
	return out
}

const maxSummary = 200

func summarize(tool string, raw json.RawMessage) string {
	var s string
	switch NormalizeTool(tool) {
	case model.ToolShell:
		s = commandText(raw)
	case model.ToolEdit:
		var paths []string
		for _, f := range patchFiles(patchText(raw), "") {
			paths = append(paths, f["path"].(string))
		}
		s = strings.Join(paths, ", ")
	default:
		s = tool
	}
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxSummary {
		s = string(r[:maxSummary-1]) + "…"
	}
	return s
}

func compact(m map[string]any) map[string]any {
	for k, v := range m {
		if s, ok := v.(string); ok && s == "" {
			delete(m, k)
		}
	}
	return m
}

// joinPath resolves p against cwd in the agent's own path style (Unix or
// Windows), independent of the OS Shiplino runs on.
func joinPath(cwd, p string) string {
	if p == "" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\\`) || (len(p) > 2 && p[1] == ':' && (p[2] == '\\' || p[2] == '/')) {
		return p
	}
	if strings.HasPrefix(cwd, "/") {
		return path.Join(cwd, p)
	}
	return strings.TrimRight(cwd, `\/`) + `\` + strings.ReplaceAll(p, "/", `\`)
}
