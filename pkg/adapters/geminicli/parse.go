package geminicli

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/elephaant/shiplino/pkg/adapters"
	"github.com/elephaant/shiplino/pkg/model"
)

// Name is the agent name used in events and spool paths.
const Name = "gemini-cli"

// Hook payloads checked against the official hooks reference
// (docs/hooks/reference.md) and packages/core/src/hooks/types.ts of
// Gemini CLI v0.63.0 on 2026-10-10. Every payload has session_id,
// transcript_path, cwd, hook_event_name and timestamp (ISO 8601).
//
//	SessionStart  session.start (source: startup | resume | clear)
//	SessionEnd    session.end (reason)
//	BeforeAgent   turn.start (prompt)
//	AfterAgent    turn.end (prompt_response)
//	BeforeTool    tool.start
//	AfterTool     tool.end + shell.exec / file.edit / file.read / mcp.call
//	Notification  waiting.start (notification_type ToolPermission)
//	PreCompress   compact (trigger)
//
// Tool hooks carry no tool call id, so their events are keyed by the
// spool envelope. The transcript has the ids (see transcript.go).

func init() { adapters.Register(Adapter{}) }

// Adapter is the Gemini CLI adapter.
type Adapter struct{}

// Name implements adapters.Adapter.
func (Adapter) Name() string { return Name }

type mcpContext struct {
	ServerName string `json:"server_name"`
	ToolName   string `json:"tool_name"`
}

type payload struct {
	SessionID        string          `json:"session_id"`
	TranscriptPath   string          `json:"transcript_path"`
	CWD              string          `json:"cwd"`
	HookEventName    string          `json:"hook_event_name"`
	Timestamp        string          `json:"timestamp"`
	Source           string          `json:"source"`
	Reason           string          `json:"reason"`
	Trigger          string          `json:"trigger"`
	Prompt           string          `json:"prompt"`
	PromptResponse   string          `json:"prompt_response"`
	ToolName         string          `json:"tool_name"`
	ToolInput        json.RawMessage `json:"tool_input"`
	ToolResponse     json.RawMessage `json:"tool_response"`
	MCP              *mcpContext     `json:"mcp_context"`
	NotificationType string          `json:"notification_type"`
	Message          string          `json:"message"`
}

// ParseHook implements adapters.Adapter.
func (Adapter) ParseHook(raw []byte, meta adapters.HookMeta) ([]model.Event, error) {
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("gemini-cli: decode hook payload: %w", err)
	}
	if p.SessionID == "" {
		return nil, fmt.Errorf("gemini-cli: hook payload has no session_id")
	}
	event := p.HookEventName
	if event == "" {
		event = meta.Event
	}
	ts := meta.ReceivedAt
	if t, err := time.Parse(time.RFC3339Nano, p.Timestamp); err == nil {
		ts = t // the agent's own clock
	}
	sid := model.SessionID(Name, p.SessionID)
	base := func(kind model.Kind, data map[string]any) model.Event {
		e := model.Event{
			ID: model.NewULID(ts), V: model.SchemaVersion, TS: ts.UTC(), ReceivedAt: meta.ReceivedAt.UTC(),
			Kind: kind, Agent: model.Agent{Name: Name}, Collector: model.CollectorHook,
			MachineID: meta.MachineID, User: meta.User, SessionID: sid, ActorID: sid, Data: data,
			DedupKey: sid + ":" + string(kind) + ":" + meta.EnvelopeID,
		}
		if p.CWD != "" {
			e.Project = &model.Project{CWD: p.CWD}
		}
		if meta.Ref != "" {
			e.Raw = &model.RawRef{Ref: meta.Ref}
		}
		return e
	}
	one := func(kind model.Kind, data map[string]any) []model.Event {
		return []model.Event{base(kind, data)}
	}

	switch event {
	case "SessionStart":
		return one(model.KindSessionStart, compact(map[string]any{"source": p.Source, "transcript_path": p.TranscriptPath})), nil
	case "SessionEnd":
		return one(model.KindSessionEnd, compact(map[string]any{"reason": p.Reason, "status": "ended"})), nil
	case "BeforeAgent":
		return one(model.KindTurnStart, compact(map[string]any{
			"prompt": p.Prompt, "prompt_chars": len([]rune(p.Prompt)), "transcript_path": p.TranscriptPath,
		})), nil
	case "AfterAgent":
		return one(model.KindTurnEnd, compact(map[string]any{"status": "ok", "assistant_summary": p.PromptResponse})), nil
	case "BeforeTool":
		return one(model.KindToolStart, map[string]any{
			"tool": NormalizeTool(p.ToolName), "tool_raw": p.ToolName, "input_summary": summarize(p.ToolName, p.ToolInput),
		}), nil
	case "AfterTool":
		var resp struct {
			LLMContent    json.RawMessage `json:"llmContent"`
			ReturnDisplay json.RawMessage `json:"returnDisplay"`
			Error         *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(p.ToolResponse, &resp)
		tc := toolCall{Name: p.ToolName, Args: p.ToolInput, OK: resp.Error == nil, Output: resp.LLMContent, Display: resp.ReturnDisplay}
		if p.MCP != nil {
			tc.MCPServer, tc.MCPTool = p.MCP.ServerName, p.MCP.ToolName
		}
		ds := tc.derive(p.CWD)
		end := base(model.KindToolEnd, map[string]any{"tool": NormalizeTool(p.ToolName), "ok": tc.ok(ds)})
		out := []model.Event{end}
		for _, d := range ds {
			e := base(d.kind, d.data)
			e.DedupKey = sid + ":" + string(d.kind) + ":" + d.suffix + ":" + meta.EnvelopeID
			out = append(out, e)
		}
		return out, nil
	case "Notification":
		if p.NotificationType != "ToolPermission" {
			return nil, nil
		}
		return one(model.KindWaitingStart, compact(map[string]any{"reason": "permission", "message": p.Message})), nil
	case "PreCompress":
		return one(model.KindCompact, compact(map[string]any{"phase": "pre", "trigger": p.Trigger})), nil
	case "BeforeModel", "AfterModel", "BeforeToolSelection":
		return nil, nil // known, never registered
	}
	return nil, fmt.Errorf("gemini-cli %q: %w", event, adapters.ErrUnknownEvent)
}

// toolCall is one finished tool call, from a hook or the transcript.
type toolCall struct {
	ID                 string
	Name               string
	Args               json.RawMessage
	OK                 bool
	Output             json.RawMessage // what the model saw (llmContent / result)
	Display            json.RawMessage // returnDisplay / resultDisplay
	MCPServer, MCPTool string
}

type derived struct {
	kind   model.Kind
	suffix string
	data   map[string]any
}

type toolArgs struct {
	FilePath string `json:"file_path"`
	DirPath  string `json:"dir_path"`
	Command  string `json:"command"`
	Content  string `json:"content"`
}

// fileDiff is Gemini CLI's display result for write_file and replace.
type fileDiff struct {
	FileDiff  string `json:"fileDiff"`
	FilePath  string `json:"filePath"`
	IsNewFile bool   `json:"isNewFile"`
	DiffStat  *struct {
		ModelAdded   int `json:"model_added_lines"`
		ModelRemoved int `json:"model_removed_lines"`
		UserAdded    int `json:"user_added_lines"`
		UserRemoved  int `json:"user_removed_lines"`
	} `json:"diffStat"`
}

// ok is false for a failed call or a command that exited non-zero.
func (tc toolCall) ok(ds []derived) bool {
	for _, d := range ds {
		if code, isInt := d.data["exit_code"].(int); isInt && code != 0 {
			return false
		}
	}
	return tc.OK
}

var exitCodeRe = regexp.MustCompile(`Exit Code: (-?\d+)`)

// derive turns a finished tool call into shell, file and MCP events.
func (tc toolCall) derive(cwd string) []derived {
	var in toolArgs
	_ = json.Unmarshal(tc.Args, &in)
	with := func(d map[string]any) map[string]any {
		if tc.ID != "" {
			d["tool_call_id"] = tc.ID
		}
		return d
	}
	switch tool := NormalizeTool(tc.Name); {
	case tool == model.ToolShell && in.Command != "":
		d := with(map[string]any{"command": in.Command})
		// The shell tool appends "Exit Code: N" to its output when N != 0.
		if m := exitCodeRe.FindAllSubmatch(tc.Output, -1); len(m) > 0 {
			var code int
			fmt.Sscan(string(m[len(m)-1][1]), &code)
			d["exit_code"] = code
		} else if tc.OK && strings.Contains(string(tc.Output), "Output: ") {
			d["exit_code"] = 0 // finished in the foreground without a failure
		}
		if dir := firstOf(joinPath(cwd, in.DirPath), cwd); dir != "" {
			d["cwd"] = dir
		}
		return []derived{{model.KindShellExec, "shell", d}}
	case tool == model.ToolEdit || tool == model.ToolWrite:
		if !tc.OK {
			return nil
		}
		var fd fileDiff
		_ = json.Unmarshal(tc.Display, &fd)
		p := firstOf(fd.FilePath, joinPath(cwd, in.FilePath))
		if p == "" {
			return nil
		}
		d := with(map[string]any{"path": p, "op": "modify"})
		if fd.IsNewFile {
			d["op"] = "create"
		}
		switch {
		case fd.DiffStat != nil: // the agent's own counts
			d["lines_added"] = fd.DiffStat.ModelAdded + fd.DiffStat.UserAdded
			d["lines_removed"] = fd.DiffStat.ModelRemoved + fd.DiffStat.UserRemoved
			d["lines_source"] = "agent"
		case fd.FileDiff != "":
			d["lines_added"], d["lines_removed"] = diffLines(fd.FileDiff)
			d["lines_source"] = "agent"
		}
		return []derived{{model.KindFileEdit, "file", d}}
	case tc.Name == "read_file" && in.FilePath != "" && tc.OK:
		return []derived{{model.KindFileRead, "file", with(map[string]any{"path": joinPath(cwd, in.FilePath)})}}
	case tool == model.ToolMCP:
		server, name := tc.MCPServer, tc.MCPTool
		if server == "" {
			server, name = splitMCP(tc.Name)
		}
		return []derived{{model.KindMCPCall, "mcp", with(map[string]any{"server": server, "tool": name, "ok": tc.OK})}}
	}
	return nil
}

// NormalizeTool maps Gemini CLI tool names to normalized tool names.
func NormalizeTool(name string) string {
	switch name {
	case "run_shell_command":
		return model.ToolShell
	case "replace", "edit":
		return model.ToolEdit
	case "write_file":
		return model.ToolWrite
	case "read_file", "read_many_files", "list_directory", "get_internal_docs", "read_mcp_resource":
		return model.ToolRead
	case "glob", "grep_search", "search_file_content":
		return model.ToolSearch
	case "google_web_search", "web_fetch":
		return model.ToolWeb
	case "invoke_agent":
		return model.ToolTask
	}
	if strings.HasPrefix(name, "mcp_") {
		return model.ToolMCP
	}
	return model.ToolOther
}

// splitMCP reads "mcp_<server>_<tool>"; like Gemini CLI, it splits at the
// first underscore after the prefix.
func splitMCP(name string) (server, tool string) {
	rest := strings.TrimPrefix(name, "mcp_")
	if i := strings.Index(rest, "_"); i > 0 {
		return rest[:i], rest[i+1:]
	}
	return rest, ""
}

const maxSummary = 200

func summarize(tool string, raw json.RawMessage) string {
	var in struct {
		toolArgs
		Pattern string `json:"pattern"`
		Query   string `json:"query"`
		URL     string `json:"url"`
		Prompt  string `json:"prompt"`
	}
	_ = json.Unmarshal(raw, &in)
	var s string
	switch NormalizeTool(tool) {
	case model.ToolShell:
		s = in.Command
	case model.ToolEdit, model.ToolWrite, model.ToolRead:
		s = firstOf(in.FilePath, in.DirPath)
	case model.ToolSearch:
		s = in.Pattern
	case model.ToolWeb:
		s = firstOf(in.Query, in.URL, in.Prompt)
	case model.ToolMCP:
		server, name := splitMCP(tool)
		s = server + "/" + name
	}
	if s == "" {
		s = tool
	}
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxSummary {
		s = string(r[:maxSummary-1]) + "…"
	}
	return s
}

func diffLines(d string) (added, removed int) {
	for _, l := range strings.Split(d, "\n") {
		switch {
		case strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"):
		case strings.HasPrefix(l, "+"):
			added++
		case strings.HasPrefix(l, "-"):
			removed++
		}
	}
	return
}

func compact(m map[string]any) map[string]any {
	for k, v := range m {
		if s, ok := v.(string); ok && s == "" {
			delete(m, k)
		}
	}
	return m
}

func firstOf(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

// joinPath resolves p against cwd in the agent's own path style (Unix or
// Windows), independent of the OS Shiplino runs on.
func joinPath(cwd, p string) string {
	if p == "" || cwd == "" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\\`) || (len(p) > 2 && p[1] == ':' && (p[2] == '\\' || p[2] == '/')) {
		return p
	}
	if strings.HasPrefix(cwd, "/") {
		return path.Join(cwd, p)
	}
	return strings.TrimRight(cwd, `\/`) + `\` + strings.ReplaceAll(p, "/", `\`)
}
