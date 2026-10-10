// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

package cline

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

// Name is the adapter name used in `shiplino hook --agent cline`.
const Name = "cline"

func init() { adapters.Register(Adapter{}) }

// Adapter is the Cline adapter.
type Adapter struct{}

// Name implements adapters.Adapter.
func (Adapter) Name() string { return Name }

// sdkEvents maps the CLI/SDK hook names to the extension's (file) names.
var sdkEvents = map[string]string{
	"agent_start": "TaskStart", "agent_resume": "TaskResume", "prompt_submit": "UserPromptSubmit",
	"tool_call": "PreToolUse", "tool_result": "PostToolUse", "agent_end": "TaskComplete",
	"agent_abort": "TaskCancel", "agent_error": "TaskError", "session_shutdown": "SessionShutdown",
	"pre_compact": "PreCompact",
}

type payload struct {
	HookName       string   `json:"hookName"`
	Timestamp      string   `json:"timestamp"`
	TaskID         string   `json:"taskId"`
	ClineVersion   string   `json:"clineVersion"`
	WorkspaceRoots []string `json:"workspaceRoots"`
	SessionContext struct {
		RootSessionID string `json:"rootSessionId"`
	} `json:"sessionContext"`
	WorkspaceInfo struct {
		RootPath string `json:"rootPath"`
		Branch   string `json:"latestGitBranchName"`
	} `json:"workspaceInfo"`
	AgentID       string `json:"agent_id"`
	ParentAgentID string `json:"parent_agent_id"`
	Model         struct {
		Slug string `json:"slug"`
	} `json:"model"`
	UserPromptSubmit *struct {
		Prompt string `json:"prompt"`
	} `json:"userPromptSubmit"`
	PostToolUse *struct {
		ToolName        string            `json:"toolName"`
		Parameters      map[string]string `json:"parameters"`
		Result          string            `json:"result"`
		Success         *bool             `json:"success"`
		ExecutionTimeMS float64           `json:"executionTimeMs"`
	} `json:"postToolUse"`
	ToolResult *struct {
		ID         string          `json:"id"`
		Name       string          `json:"name"`
		Input      json.RawMessage `json:"input"`
		Output     json.RawMessage `json:"output"`
		Error      string          `json:"error"`
		DurationMS float64         `json:"durationMs"`
		StartedAt  string          `json:"startedAt"`
		EndedAt    string          `json:"endedAt"`
	} `json:"tool_result"`
	Error *struct {
		Name    string `json:"name"`
		Message string `json:"message"`
	} `json:"error"`
	Reason string `json:"reason"`
}

// ParseHook implements adapters.Adapter.
func (Adapter) ParseHook(raw []byte, meta adapters.HookMeta) ([]model.Event, error) {
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("cline: decode hook payload: %w", err)
	}
	// The SDK's root session id stays the same across runs and subagents;
	// the extension's taskId is the task.
	id := first(p.SessionContext.RootSessionID, p.TaskID)
	if id == "" {
		return nil, fmt.Errorf("cline: hook payload has no taskId")
	}
	b := builder{p: &p, meta: meta, sid: model.SessionID(Name, id), ts: meta.ReceivedAt.UTC()}
	b.actor = b.sid
	if p.ParentAgentID != "" && p.AgentID != "" {
		b.actor = b.sid + "/sub:" + p.AgentID
	}
	// Cline's own timestamp wins over the time the hook ran.
	if t, ok := parseTime(p.Timestamp); ok {
		b.ts = t
	}
	b.cwd = p.WorkspaceInfo.RootPath
	if b.cwd == "" && len(p.WorkspaceRoots) > 0 {
		b.cwd = p.WorkspaceRoots[0]
	}

	event := first(p.HookName, meta.Event)
	if e, ok := sdkEvents[event]; ok {
		event = e
	}
	switch event {
	case "TaskStart":
		// The hosts run it before every run of a task: one start is enough.
		e := b.base(model.KindSessionStart, compact(map[string]any{"model": b.model()}))
		e.DedupKey = b.actor + ":session.start"
		return []model.Event{e}, nil
	case "TaskResume":
		return []model.Event{b.base(model.KindSessionStart, compact(map[string]any{"source": "resume", "model": b.model()}))}, nil
	case "UserPromptSubmit":
		prompt := ""
		if p.UserPromptSubmit != nil {
			prompt = p.UserPromptSubmit.Prompt
		}
		return []model.Event{b.base(model.KindTurnStart, compact(map[string]any{"prompt": prompt, "prompt_chars": len([]rune(prompt))}))}, nil
	case "PostToolUse":
		return b.tool(), nil
	case "TaskComplete":
		return []model.Event{b.base(model.KindTurnEnd, map[string]any{"status": "ok"})}, nil
	case "TaskCancel":
		return []model.Event{b.base(model.KindTurnEnd, map[string]any{"status": "interrupted"})}, nil
	case "TaskError":
		d := map[string]any{"status": "error"}
		if p.Error != nil {
			d["error"] = first(p.Error.Message, p.Error.Name)
		}
		return []model.Event{b.base(model.KindTurnEnd, compact(d))}, nil
	case "SessionShutdown":
		return []model.Event{b.base(model.KindSessionEnd, compact(map[string]any{"reason": p.Reason, "status": "ended"}))}, nil
	case "PreToolUse", "PreCompact":
		return nil, nil // not registered (see the package doc)
	default:
		return nil, fmt.Errorf("%w: cline %q", adapters.ErrUnknownEvent, event)
	}
}

// parseTime reads the extension's milliseconds since the epoch or the
// SDK's RFC 3339 time.
func parseTime(s string) (time.Time, bool) {
	if ms, err := strconv.ParseInt(s, 10, 64); err == nil && ms > 0 {
		return time.UnixMilli(ms).UTC(), true
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC(), true
	}
	return time.Time{}, false
}

type builder struct {
	p          *payload
	meta       adapters.HookMeta
	sid, actor string
	ts         time.Time
	cwd        string
}

// model is the extension's model slug; it sends "unknown" when it has none.
func (b builder) model() string {
	if s := b.p.Model.Slug; s != "unknown" {
		return s
	}
	return ""
}

func (b builder) base(kind model.Kind, data map[string]any) model.Event {
	e := model.Event{
		ID: model.NewULID(b.meta.ReceivedAt), V: model.SchemaVersion, TS: b.ts, ReceivedAt: b.meta.ReceivedAt.UTC(),
		Kind: kind, Agent: model.Agent{Name: Name, Version: b.p.ClineVersion}, Collector: model.CollectorHook,
		MachineID: b.meta.MachineID, User: b.meta.User, SessionID: b.sid, ActorID: b.actor, Data: data,
		DedupKey: b.actor + ":" + string(kind) + ":" + b.meta.EnvelopeID,
	}
	if b.actor != b.sid {
		e.ParentActor, e.ActorType = b.sid, "subagent"
	}
	if b.cwd != "" {
		e.Project = &model.Project{CWD: b.cwd, Branch: b.p.WorkspaceInfo.Branch}
	}
	if b.meta.Ref != "" {
		e.Raw = &model.RawRef{Ref: b.meta.Ref}
	}
	return e
}

// call is one finished tool call in either dialect.
type call struct {
	id, name   string
	input      map[string]json.RawMessage
	output     json.RawMessage // the SDK's structured output, if any
	result     string          // output as text
	ok         bool
	errText    string
	start, end time.Time
	durMS      int64
}

func (b builder) call() (call, bool) {
	c := call{end: b.ts, ok: true}
	switch r, pu := b.p.ToolResult, b.p.PostToolUse; {
	case r != nil && r.Name != "":
		c.id, c.name, c.output, c.durMS = r.ID, r.Name, r.Output, int64(r.DurationMS)
		c.ok, c.errText = r.Error == "", r.Error
		_ = json.Unmarshal(r.Input, &c.input)
		if json.Unmarshal(r.Output, &c.result) != nil {
			c.result = string(r.Output)
		}
		if t, ok := parseTime(r.EndedAt); ok {
			c.end = t
		}
		if t, ok := parseTime(r.StartedAt); ok {
			c.start = t
		}
	case pu != nil && pu.ToolName != "":
		c.name, c.result, c.durMS = pu.ToolName, pu.Result, int64(pu.ExecutionTimeMS)
		if pu.Success != nil {
			c.ok = *pu.Success
		}
		// Non-string parameters arrive as JSON text.
		c.input = map[string]json.RawMessage{}
		for k, v := range pu.Parameters {
			if t := strings.TrimSpace(v); (strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[")) && json.Valid([]byte(t)) {
				c.input[k] = json.RawMessage(t)
			} else {
				c.input[k], _ = json.Marshal(v)
			}
		}
	default:
		return c, false
	}
	if c.id == "" {
		c.id = "cl-" + b.meta.EnvelopeID // the extension's payload has no call id
	}
	if c.start.IsZero() || c.start.After(c.end) {
		c.start = c.end.Add(-time.Duration(c.durMS) * time.Millisecond)
	}
	return c, true
}

// NormalizeTool maps Cline tool names to normalized tool names. MCP tools
// are named <server>__<tool>.
func NormalizeTool(name string) string {
	switch name {
	case "read_files":
		return model.ToolRead
	case "editor", "apply_patch":
		return model.ToolEdit
	case "run_commands":
		return model.ToolShell
	case "search_codebase":
		return model.ToolSearch
	case "fetch_web_content":
		return model.ToolWeb
	case "spawn_agent":
		return model.ToolTask
	}
	switch {
	case strings.HasPrefix(name, "team_"):
		return model.ToolTask
	case strings.Contains(name, "__"):
		return model.ToolMCP
	}
	return model.ToolOther
}

// tool records a finished call as a start/end pair with its real timing,
// plus what it did to files, commands and MCP servers.
func (b builder) tool() []model.Event {
	c, ok := b.call()
	if !ok {
		return nil
	}
	tool := NormalizeTool(c.name)
	edits := b.edits(c)
	start := b.base(model.KindToolStart, compact(map[string]any{"tool_call_id": c.id, "tool": tool, "tool_raw": c.name, "input_summary": summary(tool, c, edits)}))
	end := b.base(model.KindToolEnd, compact(map[string]any{"tool_call_id": c.id, "tool": tool, "ok": c.ok, "duration_ms": c.durMS, "error": truncate(c.errText)}))
	start.TS, end.TS = c.start, c.end
	start.DedupKey, end.DedupKey = b.actor+":"+c.id+":start", b.actor+":"+c.id+":end"
	out := []model.Event{start, end}
	derived := func(kind model.Kind, i int, d map[string]any) {
		e := b.base(kind, d)
		e.TS = c.end
		e.DedupKey = fmt.Sprintf("%s:%s:%s:%d", b.actor, c.id, kind, i)
		out = append(out, e)
	}
	switch {
	case tool == model.ToolShell:
		codes := exitCodes(c.output)
		cmds := commands(c.input)
		for i, cmd := range cmds {
			d := map[string]any{"command": cmd, "cwd": b.cwd, "tool_call_id": c.id}
			if i < len(codes) && codes[i] != nil {
				d["exit_code"] = *codes[i]
			}
			if len(cmds) == 1 && c.durMS > 0 {
				d["duration_ms"] = c.durMS
			}
			derived(model.KindShellExec, i, compact(d))
		}
	case !c.ok:
		// A failed read, edit or MCP call changed nothing.
	case tool == model.ToolRead:
		for i, p := range readPaths(c.input) {
			derived(model.KindFileRead, i, map[string]any{"path": b.abs(p), "op": "read"})
		}
	case tool == model.ToolEdit:
		for i, d := range edits {
			derived(model.KindFileEdit, i, d)
		}
	case tool == model.ToolMCP:
		server, name, _ := strings.Cut(c.name, "__")
		derived(model.KindMCPCall, 0, compact(map[string]any{"server": server, "tool": name, "ok": true, "duration_ms": c.durMS}))
	}
	return out
}

// edits are the file edits of an editor or apply_patch call.
func (b builder) edits(c call) []map[string]any {
	switch c.name {
	case "editor":
		var in struct {
			Path       string  `json:"path"`
			OldText    *string `json:"old_text"`
			NewText    string  `json:"new_text"`
			InsertLine *int    `json:"insert_line"`
		}
		if !decode(c.input, &in) || in.Path == "" {
			return nil
		}
		d := map[string]any{"path": b.abs(in.Path), "op": "modify", "lines_source": "computed"}
		switch {
		case strings.HasPrefix(c.result, "File created successfully"):
			// The editor creates a missing file with new_text.
			d["op"], d["lines_added"], d["lines_removed"] = "create", len(splitLines(in.NewText)), 0
			if p := adapters.NewFilePatch(in.NewText); p != "" {
				d["patch"], d["patch_source"] = p, "computed"
			}
		case in.InsertLine != nil:
			d["lines_added"], d["lines_removed"] = len(splitLines(in.NewText)), 0
			d["patch"], d["patch_source"] = adapters.SnippetPatch([2]string{"", in.NewText}), "computed"
		case in.OldText != nil:
			d["lines_added"], d["lines_removed"] = lineDiff(*in.OldText, in.NewText)
			d["patch"], d["patch_source"] = adapters.SnippetPatch([2]string{*in.OldText, in.NewText}), "computed"
		default:
			delete(d, "lines_source")
		}
		return []map[string]any{d}
	case "apply_patch":
		var patch string
		if raw, ok := c.input["input"]; ok {
			_ = json.Unmarshal(raw, &patch)
		}
		return patchFiles(patch, b.abs)
	}
	return nil
}

// abs resolves a path against the workspace root, in the agent's own
// path style (the daemon may run on another OS than the fixture).
func (b builder) abs(p string) string {
	if b.cwd == "" || p == "" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\\`) || (len(p) > 2 && p[1] == ':' && (p[2] == '\\' || p[2] == '/')) {
		return p
	}
	if strings.Contains(b.cwd, `\`) {
		return strings.TrimRight(b.cwd, `\`) + `\` + strings.ReplaceAll(p, "/", `\`)
	}
	return path.Join(b.cwd, p)
}

// patchFiles reads the apply_patch format ("*** Add File: p", "*** Update
// File: p", "*** Delete File: p"; +/- lines) into file edits. Each file's
// hunks are kept as its patch: the agent's own diff.
func patchFiles(patch string, abs func(string) string) []map[string]any {
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
		line = strings.TrimSuffix(line, "\r")
		for _, h := range [][2]string{{"*** Add File: ", "create"}, {"*** Update File: ", "modify"}, {"*** Delete File: ", "delete"}} {
			if strings.HasPrefix(line, h[0]) {
				flush()
				cur = map[string]any{"path": abs(strings.TrimSpace(strings.TrimPrefix(line, h[0]))), "op": h[1], "lines_added": 0, "lines_removed": 0, "lines_source": "agent"}
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

// readPaths are the files of a read_files call, in any of the input
// shapes Cline accepts ({files: [{path}]}, {paths}, {file_paths}, …).
func readPaths(in map[string]json.RawMessage) []string {
	var out []string
	for _, k := range []string{"files", "paths", "file_paths", "path", "file_path"} {
		out = append(out, pathsOf(in[k])...)
	}
	return out
}

func pathsOf(raw json.RawMessage) []string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s == "" {
			return nil
		}
		return []string{s}
	}
	var obj struct {
		Path     string `json:"path"`
		FilePath string `json:"file_path"`
		Camel    string `json:"filePath"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		if p := first(obj.Path, obj.FilePath, obj.Camel); p != "" {
			return []string{p}
		}
		return nil
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) != nil {
		return nil
	}
	var out []string
	for _, item := range list {
		out = append(out, pathsOf(item)...)
	}
	return out
}

// commands are the command lines of a run_commands call: strings, or
// {command, args} entries run without a shell.
func commands(in map[string]json.RawMessage) []string {
	var out []string
	for _, k := range []string{"commands", "command", "cmd"} {
		out = append(out, commandsOf(in[k])...)
	}
	return out
}

func commandsOf(raw json.RawMessage) []string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s == "" {
			return nil
		}
		return []string{s}
	}
	var obj struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		if obj.Command == "" {
			return nil
		}
		return []string{strings.Join(append([]string{obj.Command}, obj.Args...), " ")}
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) != nil {
		return nil
	}
	var out []string
	for _, item := range list {
		out = append(out, commandsOf(item)...)
	}
	return out
}

var exitRe = regexp.MustCompile(`exited with code (-?\d+)`)

// exitCodes reads run_commands' structured output (SDK only): one
// {success, error} per command, in order. nil means unknown.
func exitCodes(output json.RawMessage) []*int {
	var results []struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(output, &results) != nil {
		return nil
	}
	out := make([]*int, len(results))
	for i, r := range results {
		code := 0
		if r.Success {
			out[i] = &code
		} else if m := exitRe.FindStringSubmatch(r.Error); m != nil {
			code, _ = strconv.Atoi(m[1])
			out[i] = &code
		}
	}
	return out
}

const maxSummary = 200

func summary(tool string, c call, edits []map[string]any) string {
	var parts []string
	switch tool {
	case model.ToolShell:
		parts = commands(c.input)
	case model.ToolRead:
		parts = readPaths(c.input)
	case model.ToolEdit:
		for _, e := range edits {
			parts = append(parts, e["path"].(string))
		}
	case model.ToolSearch:
		parts = commandsOf(field(c.input, "queries", "query"))
	case model.ToolWeb:
		var in struct {
			Requests []struct {
				URL string `json:"url"`
			} `json:"requests"`
		}
		decode(c.input, &in)
		for _, r := range in.Requests {
			parts = append(parts, r.URL)
		}
	case model.ToolMCP:
		_, name, _ := strings.Cut(c.name, "__")
		parts = []string{name}
	}
	return truncate(strings.Join(parts, ", "))
}

func truncate(s string) string {
	if r := []rune(s); len(r) > maxSummary {
		return string(r[:maxSummary])
	}
	return s
}

// field is the first of keys present in in.
func field(in map[string]json.RawMessage, keys ...string) json.RawMessage {
	for _, k := range keys {
		if v, ok := in[k]; ok {
			return v
		}
	}
	return nil
}

// decode re-reads a tool input map into v.
func decode(in map[string]json.RawMessage, v any) bool {
	b, err := json.Marshal(in)
	return err == nil && json.Unmarshal(b, v) == nil
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

// compact drops empty strings and zero numbers.
func compact(m map[string]any) map[string]any {
	for k, v := range m {
		switch x := v.(type) {
		case string:
			if x == "" {
				delete(m, k)
			}
		case int64:
			if x == 0 && k == "duration_ms" {
				delete(m, k)
			}
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
