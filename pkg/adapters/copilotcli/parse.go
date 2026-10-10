// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

package copilotcli

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/elephaant/shiplino/pkg/adapters"
	"github.com/elephaant/shiplino/pkg/model"
)

// Name is the adapter name used in `shiplino hook --agent copilot-cli`.
const Name = "copilot-cli"

func init() { adapters.Register(Adapter{}) }

// Adapter is the GitHub Copilot CLI adapter.
type Adapter struct{}

// Name implements adapters.Adapter.
func (Adapter) Name() string { return Name }

type payload struct {
	SessionID      string          `json:"sessionId"`
	Timestamp      json.RawMessage `json:"timestamp"`
	CWD            string          `json:"cwd"`
	HookEventName  string          `json:"hook_event_name"`
	Source         string          `json:"source"`
	Reason         string          `json:"reason"`
	Prompt         string          `json:"prompt"`
	ToolName       string          `json:"toolName"`
	ToolArgs       json.RawMessage `json:"toolArgs"`
	ToolResult     *toolResult     `json:"toolResult"`
	Error          json.RawMessage `json:"error"` // string (postToolUseFailure) or object (errorOccurred)
	ErrorContext   string          `json:"errorContext"`
	Recoverable    *bool           `json:"recoverable"`
	TranscriptPath string          `json:"transcriptPath"`
	StopReason     string          `json:"stopReason"`
	AgentID        string          `json:"agentId"`
	AgentType      string          `json:"agentType"`
	AgentName      string          `json:"agentName"`
	Trigger        string          `json:"trigger"`
	Message        string          `json:"message"`
	Title          string          `json:"title"`
	NotifType      string          `json:"notification_type"`
}

type toolResult struct {
	ResultType string `json:"resultType"`
	Text       string `json:"textResultForLlm"`
}

// toolArgs holds the argument names Copilot's built-in tools use.
type toolArgs struct {
	Command  string `json:"command"`
	Path     string `json:"path"`
	FilePath string `json:"file_path"`
	OldStr   string `json:"old_str"`
	NewStr   string `json:"new_str"`
	FileText string `json:"file_text"`
	Pattern  string `json:"pattern"`
	URL      string `json:"url"`
	Query    string `json:"query"`
	Desc     string `json:"description"`
	Input    string `json:"input"`
	Patch    string `json:"patch"`
}

// ParseHook implements adapters.Adapter.
func (Adapter) ParseHook(raw []byte, meta adapters.HookMeta) ([]model.Event, error) {
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("copilot-cli: decode hook payload: %w", err)
	}
	if p.SessionID == "" {
		return nil, fmt.Errorf("copilot-cli: hook payload has no sessionId")
	}
	event := first(meta.Event, p.HookEventName)
	b := builder{p: &p, meta: meta, sid: model.SessionID(Name, p.SessionID), ts: timestamp(p.Timestamp, meta.ReceivedAt)}

	switch event {
	case "sessionStart":
		return b.one(model.KindSessionStart, compact(map[string]any{"source": p.Source})), nil
	case "sessionEnd":
		status := "ended"
		if p.Reason == "error" || p.Reason == "timeout" {
			status = "failed"
		}
		return b.one(model.KindSessionEnd, compact(map[string]any{"reason": p.Reason, "status": status})), nil
	case "userPromptSubmitted":
		return b.one(model.KindTurnStart, compact(map[string]any{"prompt": p.Prompt, "prompt_chars": len([]rune(p.Prompt))})), nil
	case "agentStop":
		return b.one(model.KindTurnEnd, compact(map[string]any{"status": "ok", "stop_reason": p.StopReason, "transcript_path": p.TranscriptPath})), nil
	case "postToolUse":
		return b.tool(true), nil
	case "postToolUseFailure":
		return b.tool(false), nil
	case "subagentStop":
		if p.AgentID == "" {
			return nil, nil
		}
		e := b.base(model.KindSubagentEnd, compact(map[string]any{
			"child_session_id": b.sid + "/sub:" + p.AgentID, "agent_type": first(p.AgentType, p.AgentName),
			"agent_name": p.AgentName, "status": "done",
		}))
		e.DedupKey = b.sid + ":subagent.end:" + p.AgentID
		return []model.Event{e}, nil
	case "preCompact":
		return b.one(model.KindCompact, compact(map[string]any{"phase": "pre", "trigger": p.Trigger})), nil
	case "errorOccurred":
		var e struct {
			Message string `json:"message"`
			Name    string `json:"name"`
		}
		_ = json.Unmarshal(p.Error, &e)
		d := compact(map[string]any{"message": e.Message, "name": e.Name, "context": p.ErrorContext})
		if p.Recoverable != nil {
			d["recoverable"] = *p.Recoverable
		}
		return b.one(model.KindError, d), nil
	case "notification":
		reason := ""
		switch p.NotifType {
		case "permission_prompt":
			reason = "permission"
		case "elicitation_dialog":
			reason = "question"
		default: // shell/agent completion and idle: turn ends are recorded by agentStop
			return nil, nil
		}
		return b.one(model.KindWaitingStart, compact(map[string]any{
			"reason": reason, "message": first(p.Message, p.Title), "notification_type": p.NotifType,
		})), nil
	case "subagentStart", "preToolUse", "permissionRequest", "userPromptTransformed":
		return nil, nil // not registered by Shiplino; a user may still route them here
	}
	return nil, fmt.Errorf("%w: copilot-cli %q", adapters.ErrUnknownEvent, event)
}

type builder struct {
	p    *payload
	meta adapters.HookMeta
	sid  string
	ts   time.Time
}

func (b builder) base(kind model.Kind, data map[string]any) model.Event {
	e := model.Event{
		ID: model.NewULID(b.ts), V: model.SchemaVersion, TS: b.ts, ReceivedAt: b.meta.ReceivedAt.UTC(),
		Kind: kind, Agent: model.Agent{Name: Name, Surface: "cli"}, Collector: model.CollectorHook,
		MachineID: b.meta.MachineID, User: b.meta.User, SessionID: b.sid, ActorID: b.sid, Data: data,
		DedupKey: b.sid + ":" + string(kind) + ":" + b.meta.EnvelopeID,
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

// tool turns a finished tool call into a start/end pair. Copilot gives no
// tool call id, so the hook's envelope id stands in for it.
func (b builder) tool(ok bool) []model.Event {
	p := b.p
	id := "hook-" + b.meta.EnvelopeID
	tool := NormalizeTool(p.ToolName)
	args := parseArgs(p.ToolArgs)
	var patched []map[string]any
	if p.ToolName == "apply_patch" {
		patched = patchFiles(first(args.Input, args.Patch, rawString(p.ToolArgs)), p.CWD)
	}
	start := b.base(model.KindToolStart, map[string]any{
		"tool_call_id": id, "tool": tool, "tool_raw": p.ToolName, "input_summary": summarize(args, patched),
	})
	end := b.base(model.KindToolEnd, map[string]any{"tool_call_id": id, "tool": tool, "ok": ok})
	var errText string
	if !ok {
		_ = json.Unmarshal(p.Error, &errText)
		if errText != "" {
			end.Data["error"] = errText
		}
	}
	out := []model.Event{start, end}
	derived := func(kind model.Kind, suffix string, d map[string]any) {
		e := b.base(kind, d)
		e.DedupKey = b.sid + ":" + id + ":" + suffix
		out = append(out, e)
	}
	switch tool {
	case model.ToolShell:
		if p.ToolName != "bash" && p.ToolName != "powershell" {
			break // read/write/stop/list a running shell session
		}
		d := compact(map[string]any{"command": args.Command, "tool_call_id": id, "cwd": p.CWD})
		text := errText
		if p.ToolResult != nil {
			text = p.ToolResult.Text
		}
		if code, found := exitCode(text); found {
			d["exit_code"] = code
		}
		derived(model.KindShellExec, "shell", d)
	case model.ToolRead:
		if f := first(args.Path, args.FilePath); f != "" && ok {
			derived(model.KindFileRead, "file", map[string]any{"path": joinPath(p.CWD, f)})
		}
	case model.ToolEdit, model.ToolWrite:
		if !ok {
			break
		}
		if p.ToolName == "apply_patch" {
			for i, f := range patched {
				derived(model.KindFileEdit, "file:"+strconv.Itoa(i), f)
			}
			break
		}
		f := first(args.Path, args.FilePath)
		if f == "" {
			break
		}
		d := map[string]any{"path": joinPath(p.CWD, f), "op": "modify"}
		if p.ToolName == "create" {
			d["op"] = "create"
			d["lines_added"], d["lines_removed"], d["lines_source"] = lines(args.FileText), 0, "computed"
		} else if args.OldStr != "" || args.NewStr != "" {
			a, r := lineDiff(args.OldStr, args.NewStr)
			d["lines_added"], d["lines_removed"], d["lines_source"] = a, r, "computed"
		}
		derived(model.KindFileEdit, "file", d)
	}
	b.key(&out[0], id+":start")
	b.key(&out[1], id+":end")
	return out
}

func (b builder) key(e *model.Event, k string) { e.DedupKey = b.sid + ":" + k }

// timestamp reads Copilot's own timestamp (Unix ms, or ISO 8601 in the
// VS Code compatible format); the hook's receive time is the fallback.
func timestamp(raw json.RawMessage, fallback time.Time) time.Time {
	var ms int64
	if json.Unmarshal(raw, &ms) == nil && ms > 0 {
		return time.UnixMilli(ms).UTC()
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t.UTC()
		}
	}
	return fallback.UTC()
}

// parseArgs reads toolArgs, sent as an object or as a JSON string.
func parseArgs(raw json.RawMessage) toolArgs {
	var a toolArgs
	if json.Unmarshal(raw, &a) != nil {
		if s := rawString(raw); s != "" {
			_ = json.Unmarshal([]byte(s), &a)
		}
	}
	return a
}

func rawString(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

var exitRe = regexp.MustCompile(`(?i)exit(?:ed with)? code:? *(-?\d+)`)

// exitCode finds the exit code Copilot reports in a shell tool's result
// text ("<exited with exit code 1>").
func exitCode(text string) (int, bool) {
	m := exitRe.FindAllStringSubmatch(text, -1)
	if len(m) == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(m[len(m)-1][1])
	return n, err == nil
}

// NormalizeTool maps Copilot CLI tool names to normalized tool names.
func NormalizeTool(name string) string {
	switch name {
	case "bash", "powershell", "read_bash", "write_bash", "stop_bash", "list_bash",
		"read_powershell", "write_powershell", "stop_powershell", "list_powershell":
		return model.ToolShell
	case "view":
		return model.ToolRead
	case "create":
		return model.ToolWrite
	case "edit", "str_replace_editor", "apply_patch":
		return model.ToolEdit
	case "grep", "rg", "glob":
		return model.ToolSearch
	case "web_fetch", "web_search":
		return model.ToolWeb
	case "task", "read_agent", "write_agent", "list_agents":
		return model.ToolTask
	}
	return model.ToolOther
}

// summarize is a short description of a tool call for the board.
func summarize(a toolArgs, patched []map[string]any) string {
	var paths []string
	for _, f := range patched {
		paths = append(paths, f["path"].(string))
	}
	s := first(a.Command, a.Path, a.FilePath, strings.Join(paths, ", "), a.Pattern, a.URL, a.Query, a.Desc)
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200])
	}
	return s
}

// patchFiles reads the apply_patch format ("*** Add File: p", "*** Update
// File: p", "*** Delete File: p"; +/- lines) into file edits.
func patchFiles(patch, cwd string) []map[string]any {
	var out []map[string]any
	var cur map[string]any
	for _, line := range strings.Split(patch, "\n") {
		for _, h := range [][2]string{{"*** Add File: ", "create"}, {"*** Update File: ", "modify"}, {"*** Delete File: ", "delete"}} {
			if strings.HasPrefix(line, h[0]) {
				cur = map[string]any{"path": joinPath(cwd, strings.TrimSpace(strings.TrimPrefix(line, h[0]))), "op": h[1],
					"lines_added": 0, "lines_removed": 0, "lines_source": "computed"}
				out = append(out, cur)
			}
		}
		switch {
		case cur == nil || strings.HasPrefix(line, "***"):
		case strings.HasPrefix(line, "+"):
			cur["lines_added"] = cur["lines_added"].(int) + 1
		case strings.HasPrefix(line, "-"):
			cur["lines_removed"] = cur["lines_removed"].(int) + 1
		}
	}
	return out
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

func lines(s string) int { return len(splitLines(s)) }

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
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
		if s, ok := v.(string); ok && s == "" {
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
