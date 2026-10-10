package claudecode

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/elephaant/shiplino/pkg/adapters"
	"github.com/elephaant/shiplino/pkg/model"
)

// Name is the agent name used in events and spool paths.
const Name = "claude-code"

// Hook payload fields were checked against the official Claude Code hooks
// reference (https://code.claude.com/docs/en/hooks) on 2026-10-09.
//
// Native event → universal events:
//
//	SessionStart        session.start
//	SessionEnd          session.end
//	UserPromptSubmit    turn.start
//	Stop                turn.end (ok)
//	StopFailure         turn.end (error)
//	PreToolUse          tool.start
//	PostToolUse         tool.end (ok) + file.read | file.edit | shell.exec | mcp.call
//	PostToolUseFailure  tool.end (failed) + shell.exec for Bash
//	PermissionDenied    tool.end (denied)
//	PermissionRequest   waiting.start (permission)
//	Notification        waiting.start for prompts that need the user; others ignored
//	SubagentStart/Stop  subagent.start / subagent.end
//	PreCompact/PostCompact compact
//
// Other known events (TaskCreated, CwdChanged, …) are ignored for now.
// Unrecognized events return adapters.ErrUnknownEvent.

func init() { adapters.Register(Adapter{}) }

// Adapter is the Claude Code adapter.
type Adapter struct{}

// Name implements adapters.Adapter.
func (Adapter) Name() string { return Name }

type payload struct {
	CursorVersion   string          `json:"cursor_version"`
	SessionID       string          `json:"session_id"`
	PromptID        string          `json:"prompt_id"`
	TranscriptPath  string          `json:"transcript_path"`
	CWD             string          `json:"cwd"`
	PermissionMode  string          `json:"permission_mode"`
	HookEventName   string          `json:"hook_event_name"`
	AgentID         string          `json:"agent_id"`
	AgentType       string          `json:"agent_type"`
	Source          string          `json:"source"`
	Model           string          `json:"model"`
	SessionTitle    string          `json:"session_title"`
	Reason          string          `json:"reason"`
	Prompt          string          `json:"prompt"`
	ToolName        string          `json:"tool_name"`
	ToolInput       json.RawMessage `json:"tool_input"`
	ToolResponse    json.RawMessage `json:"tool_response"`
	ToolUseID       string          `json:"tool_use_id"`
	DurationMS      *int64          `json:"duration_ms"`
	Error           string          `json:"error"`
	IsInterrupt     bool            `json:"is_interrupt"`
	Message         string          `json:"message"`
	Title           string          `json:"title"`
	NotifType       string          `json:"notification_type"`
	LastMessage     string          `json:"last_assistant_message"`
	AgentTranscript string          `json:"agent_transcript_path"`
	Trigger         string          `json:"trigger"`
}

// toolResponse holds the parts of tool results Claude Code reports itself.
type toolResponse struct {
	Type            string `json:"type"` // Write: "create" | "update"
	StructuredPatch []struct {
		OldStart int      `json:"oldStart"`
		OldLines int      `json:"oldLines"`
		NewStart int      `json:"newStart"`
		NewLines int      `json:"newLines"`
		Lines    []string `json:"lines"`
	} `json:"structuredPatch"`
	Interrupted  bool  `json:"interrupted"`
	TimedOutMS   int64 `json:"timedOutAfterMs"`
	GitOperation *struct {
		Push *struct {
			Branch string `json:"branch"`
		} `json:"push"`
		PR *struct {
			Number int    `json:"number"`
			URL    string `json:"url"`
			Action string `json:"action"`
		} `json:"pr"`
		Branch *struct {
			Ref    string `json:"ref"`
			Action string `json:"action"`
		} `json:"branch"`
	} `json:"gitOperation"`
}

// patchLines counts added and removed lines in Claude Code's own diff.
func (r toolResponse) patchLines() (added, removed int, ok bool) {
	if len(r.StructuredPatch) == 0 {
		return 0, 0, false
	}
	for _, h := range r.StructuredPatch {
		for _, l := range h.Lines {
			switch {
			case strings.HasPrefix(l, "+"):
				added++
			case strings.HasPrefix(l, "-"):
				removed++
			}
		}
	}
	return added, removed, true
}

// patch renders Claude Code's own diff as patch text ("" if none).
func (r toolResponse) patch() string {
	hunks := make([]adapters.Hunk, 0, len(r.StructuredPatch))
	for _, h := range r.StructuredPatch {
		hunks = append(hunks, adapters.Hunk{OldStart: h.OldStart, OldLines: h.OldLines, NewStart: h.NewStart, NewLines: h.NewLines, Lines: h.Lines})
	}
	return adapters.FormatHunks(hunks)
}

type toolInput struct {
	FilePath     string `json:"file_path"`
	NotebookPath string `json:"notebook_path"`
	Command      string `json:"command"`
	Pattern      string `json:"pattern"`
	Path         string `json:"path"`
	URL          string `json:"url"`
	Query        string `json:"query"`
	Description  string `json:"description"`
	SubagentType string `json:"subagent_type"`
	OldString    string `json:"old_string"`
	NewString    string `json:"new_string"`
	Content      string `json:"content"`
	Edits        []struct {
		OldString string `json:"old_string"`
		NewString string `json:"new_string"`
	} `json:"edits"`
}

// ParseHook implements adapters.Adapter.
func (Adapter) ParseHook(raw []byte, meta adapters.HookMeta) ([]model.Event, error) {
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("claude-code: decode hook payload: %w", err)
	}
	// Cursor also runs Claude Code and Codex hooks, with its own payloads.
	// Cursor's native hooks record those sessions, so skip them here.
	if p.CursorVersion != "" {
		return nil, nil
	}
	if p.SessionID == "" {
		return nil, fmt.Errorf("claude-code: hook payload has no session_id")
	}
	event := p.HookEventName
	if event == "" {
		event = meta.Event
	}
	b := builder{p: &p, meta: meta, sid: model.SessionID(Name, p.SessionID)}

	switch event {
	case "SessionStart":
		data := compact(map[string]any{
			"source": p.Source, "model": p.Model, "permission_mode": p.PermissionMode,
			"title": p.SessionTitle, "transcript_path": p.TranscriptPath,
		})
		if p.SessionTitle != "" {
			data["title_source"] = "agent"
		}
		return b.one(model.KindSessionStart, data), nil
	case "SessionEnd":
		return b.one(model.KindSessionEnd, map[string]any{"reason": p.Reason, "status": "ended"}), nil
	case "UserPromptSubmit":
		data := compact(map[string]any{
			"prompt": p.Prompt, "prompt_chars": len([]rune(p.Prompt)), "transcript_path": p.TranscriptPath,
		})
		if p.SessionTitle != "" {
			data["title"], data["title_source"] = p.SessionTitle, "agent"
		}
		return b.one(model.KindTurnStart, data), nil
	case "Stop":
		return b.one(model.KindTurnEnd, compact(map[string]any{"status": "ok", "assistant_summary": p.LastMessage})), nil
	case "StopFailure":
		return b.one(model.KindTurnEnd, compact(map[string]any{"status": "error", "error": p.Error})), nil
	case "PreToolUse":
		return b.toolStart(), nil
	case "PostToolUse":
		return b.toolEnd(true, ""), nil
	case "PostToolUseFailure":
		return b.toolEnd(false, p.Error), nil
	case "PermissionDenied":
		evs := b.toolEnd(false, p.Reason)
		evs[0].Data["denied"] = true
		return evs[:1], nil
	case "PermissionRequest":
		return b.one(model.KindWaitingStart, map[string]any{
			"reason": "permission", "message": "Approve: " + summarize(p.ToolName, p.ToolInput), "tool_raw": p.ToolName,
		}), nil
	case "Notification":
		reason, ok := waitingReason(p.NotifType)
		if !ok {
			return nil, nil
		}
		return b.one(model.KindWaitingStart, compact(map[string]any{
			"reason": reason, "message": firstNonEmpty(p.Message, p.Title), "notification_type": p.NotifType,
		})), nil
	case "SubagentStart":
		return b.subagent(model.KindSubagentStart, compact(map[string]any{
			"child_session_id": b.sid + "/sub:" + p.AgentID, "agent_type": p.AgentType,
		})), nil
	case "SubagentStop":
		return b.subagent(model.KindSubagentEnd, compact(map[string]any{
			"child_session_id": b.sid + "/sub:" + p.AgentID, "agent_type": p.AgentType,
			"status": "done", "transcript_path": p.AgentTranscript,
		})), nil
	case "PreCompact":
		return b.one(model.KindCompact, map[string]any{"phase": "pre", "trigger": p.Trigger}), nil
	case "PostCompact":
		return b.one(model.KindCompact, map[string]any{"phase": "post", "trigger": p.Trigger}), nil
	case "Setup", "UserPromptExpansion", "PostToolBatch", "MessageDisplay", "TaskCreated", "TaskCompleted",
		"TeammateIdle", "InstructionsLoaded", "ConfigChange", "CwdChanged", "DirectoryAdded", "FileChanged",
		"WorktreeCreate", "WorktreeRemove", "PreModelSwitch", "PostModelSwitch", "Elicitation", "ElicitationResult":
		return nil, nil
	}
	return nil, fmt.Errorf("claude-code %q: %w", event, adapters.ErrUnknownEvent)
}

type builder struct {
	p    *payload
	meta adapters.HookMeta
	sid  string
}

// base fills the fields every event from this payload shares.
func (b builder) base(kind model.Kind, data map[string]any) model.Event {
	e := model.Event{
		ID:         model.NewULID(b.meta.ReceivedAt),
		V:          model.SchemaVersion,
		TS:         b.meta.ReceivedAt.UTC(),
		ReceivedAt: b.meta.ReceivedAt.UTC(),
		Kind:       kind,
		Agent:      model.Agent{Name: Name},
		Collector:  model.CollectorHook,
		MachineID:  b.meta.MachineID,
		User:       b.meta.User,
		SessionID:  b.sid,
		ActorID:    b.sid,
		TurnID:     b.p.PromptID,
		Data:       data,
		DedupKey:   b.sid + ":" + string(kind) + ":" + b.meta.EnvelopeID,
	}
	if b.p.AgentID != "" {
		e.ActorID = b.sid + "/sub:" + b.p.AgentID
		e.ParentActor = b.sid
		e.ActorType = b.p.AgentType
	} else if b.p.AgentType != "" {
		e.ActorType = b.p.AgentType // main session started with --agent
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

// subagent events are reported by the parent; the child is in the data.
func (b builder) subagent(kind model.Kind, data map[string]any) []model.Event {
	e := b.base(kind, data)
	e.ActorID, e.ParentActor, e.ActorType = b.sid, "", ""
	return []model.Event{e}
}

func (b builder) toolKey(suffix string) string {
	return b.sid + ":" + b.p.ToolUseID + ":" + suffix
}

func (b builder) toolStart() []model.Event {
	e := b.base(model.KindToolStart, map[string]any{
		"tool_call_id":  b.p.ToolUseID,
		"tool":          NormalizeTool(b.p.ToolName),
		"tool_raw":      b.p.ToolName,
		"input_summary": summarize(b.p.ToolName, b.p.ToolInput),
	})
	if b.p.ToolUseID != "" {
		e.DedupKey = b.toolKey("start")
	}
	return []model.Event{e}
}

func (b builder) toolEnd(ok bool, errMsg string) []model.Event {
	data := map[string]any{"tool_call_id": b.p.ToolUseID, "tool": NormalizeTool(b.p.ToolName), "ok": ok}
	if b.p.DurationMS != nil {
		data["duration_ms"] = *b.p.DurationMS
	}
	if errMsg != "" {
		data["error"] = errMsg
	}
	if b.p.IsInterrupt {
		data["interrupted"] = true
	}
	end := b.base(model.KindToolEnd, data)
	if b.p.ToolUseID != "" {
		end.DedupKey = b.toolKey("end")
	}
	out := []model.Event{end}

	var in toolInput
	_ = json.Unmarshal(b.p.ToolInput, &in)
	var resp toolResponse
	_ = json.Unmarshal(b.p.ToolResponse, &resp)
	if resp.Interrupted {
		end.Data["interrupted"] = true
	}
	if resp.TimedOutMS > 0 {
		end.Data["timed_out_ms"] = resp.TimedOutMS
	}
	// fileEdit prefers Claude Code's own diff; counting the strings in the
	// tool input (and a patch built from them) is only the fallback.
	fileEdit := func(path, op string, added, removed int, patch string) map[string]any {
		d := map[string]any{"path": path, "op": op, "lines_added": added, "lines_removed": removed, "lines_source": "estimated"}
		if a, r, ok := resp.patchLines(); ok {
			d["lines_added"], d["lines_removed"], d["lines_source"] = a, r, "agent"
			if p := resp.patch(); p != "" {
				d["patch"], d["patch_source"] = p, "agent"
				return d
			}
		}
		if patch != "" {
			d["patch"], d["patch_source"] = patch, "computed"
		}
		return d
	}
	derived := func(kind model.Kind, suffix string, d map[string]any) {
		e := b.base(kind, d)
		if b.p.ToolUseID != "" {
			e.DedupKey = b.toolKey(suffix)
		}
		out = append(out, e)
	}

	switch tool := b.p.ToolName; {
	case tool == "Bash":
		d := map[string]any{"command": in.Command, "tool_call_id": b.p.ToolUseID}
		if ok {
			d["exit_code"] = 0
		} else if code, found := exitCode(errMsg); found {
			d["exit_code"] = code
		}
		if b.p.DurationMS != nil {
			d["duration_ms"] = *b.p.DurationMS
		}
		if b.p.CWD != "" {
			d["cwd"] = b.p.CWD
		}
		if resp.Interrupted {
			d["interrupted"] = true
		}
		derived(model.KindShellExec, "shell", d)
		if g := resp.GitOperation; g != nil && ok {
			switch {
			case g.PR != nil && g.PR.URL != "":
				derived(model.KindGitPR, "git", map[string]any{"number": g.PR.Number, "url": g.PR.URL, "action": g.PR.Action, "source": "agent"})
			case g.Push != nil:
				derived(model.KindGitPush, "git", map[string]any{"branch": g.Push.Branch, "source": "agent"})
			case g.Branch != nil:
				derived(model.KindGitBranch, "git", map[string]any{"to": g.Branch.Ref, "action": g.Branch.Action, "source": "agent"})
			}
		}
	case !ok:
		// Failed non-shell tools changed nothing.
	case tool == "Read" || tool == "NotebookRead":
		if path := firstNonEmpty(in.FilePath, in.NotebookPath); path != "" {
			derived(model.KindFileRead, "file", map[string]any{"path": path})
		}
	case tool == "Edit":
		derived(model.KindFileEdit, "file", fileEdit(in.FilePath, "modify", lines(in.NewString), lines(in.OldString),
			adapters.SnippetPatch([2]string{in.OldString, in.NewString})))
	case tool == "MultiEdit":
		added, removed := 0, 0
		pairs := make([][2]string, 0, len(in.Edits))
		for _, ed := range in.Edits {
			added += lines(ed.NewString)
			removed += lines(ed.OldString)
			pairs = append(pairs, [2]string{ed.OldString, ed.NewString})
		}
		derived(model.KindFileEdit, "file", fileEdit(in.FilePath, "modify", added, removed, adapters.SnippetPatch(pairs...)))
	case tool == "NotebookEdit":
		derived(model.KindFileEdit, "file", map[string]any{"path": in.NotebookPath, "op": "modify"})
	case tool == "Write":
		if resp.Type == "create" {
			// A new file: every line of its content was added (exact).
			d := map[string]any{"path": in.FilePath, "op": "create", "lines_added": lines(in.Content), "lines_removed": 0, "lines_source": "agent"}
			if p := adapters.NewFilePatch(in.Content); p != "" {
				d["patch"], d["patch_source"] = p, "computed"
			}
			derived(model.KindFileEdit, "file", d)
		} else {
			derived(model.KindFileEdit, "file", fileEdit(in.FilePath, "modify", lines(in.Content), 0, ""))
		}
	case strings.HasPrefix(tool, "mcp__"):
		server, name := splitMCP(tool)
		d := map[string]any{"server": server, "tool": name, "ok": ok}
		if b.p.DurationMS != nil {
			d["duration_ms"] = *b.p.DurationMS
		}
		derived(model.KindMCPCall, "mcp", d)
	}
	return out
}

// NormalizeTool maps a Claude Code tool name to a normalized tool name.
func NormalizeTool(name string) string {
	switch name {
	case "Edit", "MultiEdit", "NotebookEdit":
		return model.ToolEdit
	case "Write":
		return model.ToolWrite
	case "Read", "NotebookRead":
		return model.ToolRead
	case "Bash", "BashOutput", "KillShell", "KillBash", "PowerShell":
		return model.ToolShell
	case "Grep", "Glob", "LS", "ToolSearch":
		return model.ToolSearch
	case "WebFetch", "WebSearch":
		return model.ToolWeb
	case "Task", "Agent":
		return model.ToolTask
	}
	if strings.HasPrefix(name, "mcp__") {
		return model.ToolMCP
	}
	return model.ToolOther
}

const maxSummary = 200

// summarize gives a short, human-readable description of a tool call.
func summarize(tool string, raw json.RawMessage) string {
	var in toolInput
	_ = json.Unmarshal(raw, &in)
	var s string
	switch NormalizeTool(tool) {
	case model.ToolShell:
		s = in.Command
	case model.ToolEdit, model.ToolWrite, model.ToolRead:
		s = firstNonEmpty(in.FilePath, in.NotebookPath)
	case model.ToolSearch:
		s = firstNonEmpty(in.Pattern, in.Path)
	case model.ToolWeb:
		s = firstNonEmpty(in.URL, in.Query)
	case model.ToolTask:
		s = firstNonEmpty(in.Description, in.SubagentType)
	case model.ToolMCP:
		server, name := splitMCP(tool)
		s = server + "/" + name
	default:
		s = tool
	}
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxSummary {
		s = string(r[:maxSummary-1]) + "…"
	}
	return s
}

// waitingReason maps notification types that mean "the agent needs you".
func waitingReason(t string) (string, bool) {
	switch t {
	case "permission_prompt":
		return "permission", true
	case "idle_prompt", "agent_needs_input":
		return "question", true
	case "elicitation_dialog", "elicitation_url_dialog":
		return "question", true
	}
	return "", false
}

func splitMCP(tool string) (server, name string) {
	rest := strings.TrimPrefix(tool, "mcp__")
	server, name, found := strings.Cut(rest, "__")
	if !found {
		return rest, ""
	}
	return server, name
}

// exitCode parses "Exit code N" at the start of a Bash failure message.
func exitCode(msg string) (int, bool) {
	const prefix = "Exit code "
	if !strings.HasPrefix(msg, prefix) {
		return 0, false
	}
	digits := msg[len(prefix):]
	end := 0
	for end < len(digits) && digits[end] >= '0' && digits[end] <= '9' {
		end++
	}
	n, err := strconv.Atoi(digits[:end])
	return n, err == nil
}

func lines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

// compact drops empty strings so events don't carry noise.
func compact(m map[string]any) map[string]any {
	for k, v := range m {
		if s, ok := v.(string); ok && s == "" {
			delete(m, k)
		}
	}
	return m
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
