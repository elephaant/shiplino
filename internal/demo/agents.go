package demo

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/elephaant/shiplino/internal/spool"
	"github.com/elephaant/shiplino/pkg/model"
)

// Agent names, as the adapters register them.
const (
	claude = "claude-code"
	codex  = "codex"
	cursor = "cursor"
	gemini = "gemini-cli"
)

// writer puts synthetic agent output where the real agents put theirs:
// hook payloads in the spool (as the hook shim writes them) and
// transcripts under the demo's own home folder. The daemon then reads
// them with the real adapters, so the demo follows format changes.
type writer struct {
	spool string // spool root
	home  string // the demo's user home (transcripts)

	mu sync.Mutex // the live loop writes from several goroutines
}

// hook appends one hook payload to the session's spool file, stamped with
// the time the hook "ran".
func (w *writer) hook(agent, session string, at time.Time, p map[string]any) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	event, _ := p["hook_event_name"].(string)
	env := spool.Envelope{ID: model.NewULID(at), Agent: agent, Event: event, TS: at.UnixNano(), PID: 4242}
	w.mu.Lock()
	defer w.mu.Unlock()
	return spool.Write(w.spool, session, env, b)
}

// line appends one JSON line to a transcript file.
func (w *writer) line(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(b, '\n'))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// todo is one item of an agent's todo list.
type todo struct {
	text   string
	status string // pending | in_progress | completed
}

// session is one synthetic agent session. Its methods record one step
// each, in the native format of its agent; the first error sticks and
// later steps do nothing.
type session struct {
	w       *writer
	agent   string
	id      string
	project *project
	model   string

	transcript string // the agent's own session file, where it has one
	turn       string // current prompt / turn / generation id
	n          int    // counter for tool, message and turn ids
	limit5h    float64
	prompted   bool // the next work() continues a turn already started

	// The session's clock: steps happen at now, pace apart. Seeded
	// history runs on virtual time; live sessions (live != nil) wait.
	now  time.Time
	pace time.Duration
	live context.Context
	r    *rand.Rand
	prs  *counter

	err error // the first write error; later steps do nothing
}

func newSession(w *writer, agent, id string, p *project, model string) *session {
	s := &session{w: w, agent: agent, id: id, project: p, model: model}
	switch agent {
	case claude:
		s.transcript = filepath.Join(w.home, ".claude", "projects", strings.ReplaceAll(p.cwd(), "/", "-"), id+".jsonl")
	case codex:
		s.transcript = filepath.Join(w.home, ".codex", "sessions", "demo", "rollout-"+id+".jsonl")
	case gemini:
		s.transcript = filepath.Join(w.home, ".gemini", "tmp", p.name, "chats", "session-"+id+".jsonl")
	}
	return s
}

func (s *session) next(prefix string) string {
	s.n++
	return fmt.Sprintf("%s%s%d", prefix, s.id[len(s.id)-4:], s.n)
}

func (s *session) path(rel string) string { return s.project.cwd() + "/" + rel }

func stamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// hook sends one hook payload with the fields every payload of the agent
// carries.
func (s *session) hook(at time.Time, event string, fields map[string]any) {
	if s.err != nil {
		return
	}
	p := map[string]any{"hook_event_name": event}
	switch s.agent {
	case claude:
		p["session_id"], p["transcript_path"], p["cwd"], p["permission_mode"] = s.id, s.transcript, s.project.cwd(), "default"
		if s.turn != "" {
			p["prompt_id"] = s.turn
		}
	case codex:
		p["session_id"], p["transcript_path"], p["cwd"], p["model"] = s.id, s.transcript, s.project.cwd(), s.model
		if s.turn != "" {
			p["turn_id"] = s.turn
		}
	case cursor:
		p["conversation_id"], p["generation_id"], p["model"] = s.id, s.turn, s.model
		p["cursor_version"], p["workspace_roots"], p["cwd"] = "2026.09.02", []string{s.project.cwd()}, s.project.cwd()
	case gemini:
		p["session_id"], p["transcript_path"], p["cwd"], p["timestamp"] = s.id, s.transcript, s.project.cwd(), stamp(at)
	}
	for k, v := range fields {
		p[k] = v
	}
	s.err = s.w.hook(s.agent, s.id, at, p)
}

func (s *session) record(path string, v map[string]any) {
	if s.err == nil {
		s.err = s.w.line(path, v)
	}
}

// start opens the session.
func (s *session) start(at time.Time) {
	switch s.agent {
	case claude:
		s.hook(at, "SessionStart", map[string]any{"source": "startup", "model": s.model})
	case codex:
		s.record(s.transcript, map[string]any{"timestamp": stamp(at), "type": "session_meta", "payload": map[string]any{
			"session_id": s.id, "id": s.id, "cwd": s.project.cwd(), "cli_version": "0.153.4", "originator": "codex_cli_rs",
		}})
		s.record(s.transcript, map[string]any{"timestamp": stamp(at), "type": "turn_context", "payload": map[string]any{"cwd": s.project.cwd(), "model": s.model}})
		s.hook(at, "SessionStart", map[string]any{"source": "startup", "permission_mode": "default"})
	case cursor:
		s.hook(at, "sessionStart", map[string]any{"session_id": s.id, "composer_mode": "agent", "is_background_agent": false})
	case gemini:
		s.record(s.transcript, map[string]any{"sessionId": s.id, "projectHash": s.project.name, "startTime": stamp(at), "lastUpdated": stamp(at), "kind": "main"})
		s.hook(at, "SessionStart", map[string]any{"source": "startup"})
	}
}

// prompt starts a turn.
func (s *session) prompt(at time.Time, text string) {
	s.turn = s.next("t")
	switch s.agent {
	case claude:
		s.hook(at, "UserPromptSubmit", map[string]any{"prompt": text})
		s.record(s.transcript, map[string]any{"type": "user", "sessionId": s.id, "timestamp": stamp(at), "cwd": s.project.cwd(),
			"version": "2.1.30", "uuid": s.next("u"), "message": map[string]any{"role": "user", "content": text}})
	case codex:
		s.hook(at, "UserPromptSubmit", map[string]any{"prompt": text})
	case cursor:
		s.hook(at, "beforeSubmitPrompt", map[string]any{"prompt": text, "attachments": []any{}})
	case gemini:
		s.hook(at, "BeforeAgent", map[string]any{"prompt": text})
		s.record(s.transcript, map[string]any{"id": s.next("m"), "timestamp": stamp(at), "type": "user", "content": []any{map[string]any{"text": text}}})
	}
}

// tool records one finished tool call that took d, in Claude Code's
// format: PreToolUse at `at`, then PostToolUse (or PostToolUseFailure).
func (s *session) claudeTool(at time.Time, d time.Duration, agentID, name string, input, response map[string]any, failure string) {
	id := s.next("toolu_")
	f := map[string]any{"tool_name": name, "tool_input": input, "tool_use_id": id}
	if agentID != "" {
		f["agent_id"], f["agent_type"] = agentID, "Explore"
	}
	s.hook(at, "PreToolUse", f)
	end := map[string]any{"duration_ms": d.Milliseconds()}
	for k, v := range f {
		end[k] = v
	}
	if failure != "" {
		end["error"] = failure
		s.hook(at.Add(d), "PostToolUseFailure", end)
		return
	}
	end["tool_response"] = response
	s.hook(at.Add(d), "PostToolUse", end)
}

// codexTool records a finished Codex tool call.
func (s *session) codexTool(at time.Time, d time.Duration, name string, input map[string]any, response any) {
	id := s.next("call_")
	f := map[string]any{"tool_name": name, "tool_use_id": id, "tool_input": input}
	s.hook(at, "PreToolUse", f)
	end := map[string]any{"tool_response": response}
	for k, v := range f {
		end[k] = v
	}
	s.hook(at.Add(d), "PostToolUse", end)
}

// geminiTool records a finished Gemini CLI tool call.
func (s *session) geminiTool(at time.Time, d time.Duration, name string, args map[string]any, response map[string]any) {
	s.hook(at, "BeforeTool", map[string]any{"tool_name": name, "tool_input": args})
	s.hook(at.Add(d), "AfterTool", map[string]any{"tool_name": name, "tool_input": args, "tool_response": response})
}

// cursorTool records a finished Cursor tool call (Cursor reports it once
// it's done, with its duration).
func (s *session) cursorTool(at time.Time, d time.Duration, name string, input map[string]any, output string, failure string) {
	f := map[string]any{"tool_name": name, "tool_input": input, "tool_use_id": s.next("tu-"), "duration": d.Milliseconds()}
	if failure != "" {
		f["error_message"], f["failure_type"], f["is_interrupt"] = failure, "error", false
		s.hook(at.Add(d), "postToolUseFailure", f)
		return
	}
	f["tool_output"] = output
	s.hook(at.Add(d), "postToolUse", f)
}

// search looks for a pattern in the project.
func (s *session) search(at time.Time, pattern string) {
	d := 300 * time.Millisecond
	switch s.agent {
	case claude:
		s.claudeTool(at, d, "", "Grep", map[string]any{"pattern": pattern, "path": "src"}, map[string]any{"numFiles": 3}, "")
	case codex:
		s.codexTool(at, d, "exec", map[string]any{"command": []string{"bash", "-lc", "rg -n " + pattern + " src"}}, map[string]any{"exit_code": 0})
	case cursor:
		s.cursorTool(at, d, "Grep", map[string]any{"pattern": pattern, "path": "src"}, `{"matches":3}`, "")
	case gemini:
		s.geminiTool(at, d, "search_file_content", map[string]any{"pattern": pattern}, map[string]any{"llmContent": "Found 3 matches"})
	}
}

// read reads one file.
func (s *session) read(at time.Time, rel string) {
	d := 40 * time.Millisecond
	switch s.agent {
	case claude:
		s.claudeTool(at, d, "", "Read", map[string]any{"file_path": s.path(rel)}, map[string]any{"type": "text"}, "")
	case codex:
		s.codexTool(at, d, "exec", map[string]any{"command": []string{"bash", "-lc", "sed -n 1,120p " + rel}}, map[string]any{"exit_code": 0})
	case cursor:
		s.cursorTool(at, d, "Read", map[string]any{"path": s.path(rel)}, "{}", "")
	case gemini:
		s.geminiTool(at, d, "read_file", map[string]any{"file_path": s.path(rel)}, map[string]any{"llmContent": "(file contents)"})
	}
}

// edit changes a file by `added` and `removed` lines.
func (s *session) edit(at time.Time, rel string, added, removed int) {
	d := 120 * time.Millisecond
	oldText, newText := fakeLines("before", removed), fakeLines("after", added)
	switch s.agent {
	case claude:
		lines := make([]string, 0, added+removed)
		for _, l := range strings.Split(strings.TrimSuffix(oldText, "\n"), "\n") {
			if removed > 0 {
				lines = append(lines, "-"+l)
			}
		}
		for _, l := range strings.Split(strings.TrimSuffix(newText, "\n"), "\n") {
			if added > 0 {
				lines = append(lines, "+"+l)
			}
		}
		patch := []any{map[string]any{"oldStart": 10, "oldLines": removed, "newStart": 10, "newLines": added, "lines": lines}}
		s.claudeTool(at, d, "", "Edit", map[string]any{"file_path": s.path(rel), "old_string": oldText, "new_string": newText},
			map[string]any{"filePath": s.path(rel), "structuredPatch": patch}, "")
	case codex:
		var b strings.Builder
		b.WriteString("*** Begin Patch\n*** Update File: " + rel + "\n@@\n")
		for _, l := range strings.Split(strings.TrimSuffix(oldText, "\n"), "\n") {
			if removed > 0 {
				b.WriteString("-" + l + "\n")
			}
		}
		for _, l := range strings.Split(strings.TrimSuffix(newText, "\n"), "\n") {
			if added > 0 {
				b.WriteString("+" + l + "\n")
			}
		}
		b.WriteString("*** End Patch")
		s.codexTool(at, d, "apply_patch", map[string]any{"input": b.String()}, map[string]any{"success": true})
	case cursor:
		s.hook(at.Add(d), "afterFileEdit", map[string]any{"file_path": rel, "edits": []any{map[string]any{"old_string": oldText, "new_string": newText}}})
		s.cursorTool(at, d, "StrReplace", map[string]any{"path": s.path(rel), "old_string": "…", "new_string": "…"}, "{}", "")
	case gemini:
		s.geminiTool(at, d, "replace", map[string]any{"file_path": s.path(rel), "old_string": oldText, "new_string": newText}, map[string]any{
			"llmContent": "Successfully modified file: " + s.path(rel),
			"returnDisplay": map[string]any{"fileName": filepath.Base(rel), "filePath": s.path(rel), "diffStat": map[string]any{
				"model_added_lines": added, "model_removed_lines": removed, "user_added_lines": 0, "user_removed_lines": 0,
			}},
		})
	}
}

// fakeLines returns n placeholder source lines.
func fakeLines(kind string, n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "  // %s %d\n", kind, i+1)
	}
	return b.String()
}

// shell runs a command that exits with code after d.
func (s *session) shell(at time.Time, d time.Duration, cmd string, code int) {
	switch s.agent {
	case claude:
		if code != 0 {
			s.claudeTool(at, d, "", "Bash", map[string]any{"command": cmd}, nil, fmt.Sprintf("Exit code %d\n1 failing", code))
			return
		}
		s.claudeTool(at, d, "", "Bash", map[string]any{"command": cmd}, map[string]any{"stdout": "ok", "stderr": "", "interrupted": false}, "")
	case codex:
		s.codexTool(at, d, "exec", map[string]any{"command": []string{"bash", "-lc", cmd}}, map[string]any{"exit_code": code})
	case cursor:
		in := map[string]any{"command": cmd, "working_directory": s.project.cwd()}
		if code != 0 {
			s.cursorTool(at, d, "Shell", in, "", fmt.Sprintf("exit status %d", code))
			return
		}
		s.cursorTool(at, d, "Shell", in, `{"exitCode":0,"stdout":"ok"}`, "")
	case gemini:
		out := "Output: ok"
		if code != 0 {
			out = fmt.Sprintf("Output: 1 failing\nExit Code: %d", code)
		}
		s.geminiTool(at, d, "run_shell_command", map[string]any{"command": cmd}, map[string]any{"llmContent": out})
	}
}

// todos replaces the agent's todo list.
func (s *session) todos(at time.Time, items []todo) {
	d := 10 * time.Millisecond
	switch s.agent {
	case claude:
		list := make([]any, len(items))
		for i, t := range items {
			list[i] = map[string]any{"content": t.text, "status": t.status, "activeForm": t.text}
		}
		s.claudeTool(at, d, "", "TodoWrite", map[string]any{"todos": list}, map[string]any{"oldTodos": []any{}, "newTodos": []any{}}, "")
	case codex:
		list := make([]any, len(items))
		for i, t := range items {
			list[i] = map[string]any{"step": t.text, "status": t.status}
		}
		s.codexTool(at, d, "update_plan", map[string]any{"plan": list}, "Plan updated")
	case cursor:
		list := make([]any, len(items))
		for i, t := range items {
			list[i] = map[string]any{"id": fmt.Sprintf("t%d", i+1), "content": t.text, "status": t.status}
		}
		s.cursorTool(at, d, "TodoWrite", map[string]any{"merge": false, "todos": list}, "{}", "")
	case gemini:
		list := make([]any, len(items))
		for i, t := range items {
			list[i] = map[string]any{"description": t.text, "status": t.status}
		}
		s.geminiTool(at, d, "write_todos", map[string]any{"todos": list}, map[string]any{"llmContent": "Updated the todo list"})
	}
}

// wait asks the user for permission to run cmd.
func (s *session) wait(at time.Time, cmd string) {
	switch s.agent {
	case claude:
		s.hook(at, "Notification", map[string]any{"message": "Claude needs your permission to use Bash", "notification_type": "permission_prompt"})
	case codex:
		s.hook(at, "PermissionRequest", map[string]any{"tool_name": "exec", "tool_input": map[string]any{"command": []string{"bash", "-lc", cmd}}})
	case gemini:
		s.hook(at, "Notification", map[string]any{"notification_type": "ToolPermission", "message": "Tool Shell requires confirmation",
			"details": map[string]any{"type": "exec", "title": "Confirm Shell Command", "command": cmd}})
	}
	// Cursor has no hook that reports waiting without being able to block.
}

// notify sends a Claude Code Notification of the given type, e.g.
// "elicitation_dialog" (a question) or "idle_prompt" (done, waiting for
// the next prompt).
func (s *session) notify(at time.Time, typ, msg string) {
	if s.agent == claude {
		s.hook(at, "Notification", map[string]any{"message": msg, "notification_type": typ})
	}
}

// usage records one model response: its text and tokens, in the agent's
// own session file (Cursor reports them in a hook).
func (s *session) usage(at time.Time, text string, in, out, cached int64) {
	id := s.next("msg_")
	switch s.agent {
	case claude:
		s.record(s.transcript, map[string]any{"type": "assistant", "sessionId": s.id, "timestamp": stamp(at), "cwd": s.project.cwd(), "version": "2.1.30",
			"message": map[string]any{"id": id, "model": s.model, "role": "assistant", "type": "message",
				"content": []any{map[string]any{"type": "text", "text": text}},
				"usage":   map[string]any{"input_tokens": in, "output_tokens": out, "cache_read_input_tokens": cached, "cache_creation_input_tokens": in / 4}}})
	case codex:
		s.record(s.transcript, map[string]any{"timestamp": stamp(at), "type": "token_usage_record", "payload": map[string]any{
			"session_id": s.id, "turn_id": s.turn, "response_id": id,
			"usage": map[string]any{"input_tokens": in + cached, "cached_input_tokens": cached, "cache_write_input_tokens": 0, "output_tokens": out, "reasoning_output_tokens": out / 3, "total_tokens": in + cached + out},
		}})
	case cursor:
		s.hook(at, "afterAgentResponse", map[string]any{"text": text, "input_tokens": in, "output_tokens": out, "cache_read_tokens": cached})
	case gemini:
		s.record(s.transcript, map[string]any{"id": id, "timestamp": stamp(at), "type": "gemini", "content": text, "model": s.model, "thoughts": []any{},
			"tokens": map[string]any{"input": in + cached, "output": out, "cached": cached, "thoughts": out / 4, "tool": 0, "total": in + cached + out}})
	}
}

// codexLimits records Codex's plan usage windows (5-hour and weekly).
func (s *session) codexLimits(at time.Time, fiveHour, weekly float64) {
	if s.agent != codex {
		return
	}
	reset5 := at.Truncate(time.Hour).Add(4 * time.Hour)
	reset7 := at.Truncate(24 * time.Hour).Add(4 * 24 * time.Hour)
	s.record(s.transcript, map[string]any{"timestamp": stamp(at), "type": "event_msg", "payload": map[string]any{
		"type": "token_count", "info": map[string]any{},
		"rate_limits": map[string]any{"limit_id": "codex", "plan_type": "plus",
			"primary":   map[string]any{"used_percent": fiveHour, "window_minutes": 300, "resets_at": reset5.Unix()},
			"secondary": map[string]any{"used_percent": weekly, "window_minutes": 10080, "resets_at": reset7.Unix()},
		},
	}})
}

// limitReached records Claude Code refusing a request at its 5-hour
// limit, the way Claude Code writes it to the transcript.
func (s *session) limitReached(at, resets time.Time) {
	if s.agent != claude {
		return
	}
	s.record(s.transcript, map[string]any{"type": "assistant", "sessionId": s.id, "timestamp": stamp(at), "cwd": s.project.cwd(), "version": "2.1.30",
		"error": "rate_limit", "isApiErrorMessage": true,
		"quotaLimits": map[string]any{"rateLimitType": "five_hour", "status": "rejected", "resetsAt": resets.Unix()},
		"message": map[string]any{"id": s.next("msg_"), "model": "<synthetic>", "role": "assistant", "type": "message",
			"content": []any{map[string]any{"type": "text", "text": "You've hit your session limit."}}}})
}

// subagent runs a helper that searches and reads, then reports back.
func (s *session) subagent(at time.Time, task string, reads []string) time.Time {
	switch s.agent {
	case claude:
		aid, call := s.next("a"), s.next("toolu_")
		taskCall := map[string]any{"tool_name": "Task", "tool_input": map[string]any{"description": task, "subagent_type": "Explore"}, "tool_use_id": call}
		s.hook(at, "PreToolUse", taskCall)
		s.hook(at, "SubagentStart", map[string]any{"agent_id": aid, "agent_type": "Explore"})
		path := filepath.Join(strings.TrimSuffix(s.transcript, ".jsonl"), "subagents", "agent-"+aid+".jsonl")
		t := at.Add(2 * time.Second)
		for _, r := range reads {
			s.claudeTool(t, 50*time.Millisecond, aid, "Read", map[string]any{"file_path": s.path(r)}, map[string]any{"type": "text"}, "")
			s.record(path, map[string]any{"type": "assistant", "sessionId": s.id, "agentId": aid, "isSidechain": true, "timestamp": stamp(t), "cwd": s.project.cwd(), "version": "2.1.30",
				"message": map[string]any{"id": s.next("msg_"), "model": "claude-haiku-5-5", "role": "assistant", "type": "message",
					"content": []any{map[string]any{"type": "text", "text": "Reading " + r}},
					"usage":   map[string]any{"input_tokens": 9000, "output_tokens": 1400, "cache_read_input_tokens": 60000, "cache_creation_input_tokens": 8000}}})
			t = t.Add(3 * time.Second)
		}
		s.hook(t, "SubagentStop", map[string]any{"agent_id": aid, "agent_type": "Explore", "agent_transcript_path": path, "last_assistant_message": "Found the relevant code."})
		taskCall["tool_response"] = map[string]any{"status": "completed"}
		s.hook(t, "PostToolUse", taskCall)
		return t
	case codex:
		aid := s.next("sa-")
		s.hook(at, "SubagentStart", map[string]any{"agent_id": aid, "agent_type": "explorer"})
		t := at.Add(time.Duration(3*len(reads)+2) * time.Second)
		s.hook(t, "SubagentStop", map[string]any{"agent_id": aid, "agent_type": "explorer", "last_assistant_message": "Found it.", "stop_hook_active": false})
		return t
	}
	return at
}

// pr opens a pull request with gh, as Claude Code reports it.
func (s *session) pr(at time.Time, number int) {
	if s.agent != claude {
		return
	}
	url := prURL(s.project, number)
	s.claudeTool(at, 2*time.Second, "", "Bash", map[string]any{"command": "gh pr create --fill"}, map[string]any{
		"stdout": url, "stderr": "", "interrupted": false,
		"gitOperation": map[string]any{"pr": map[string]any{"number": number, "url": url, "action": "created"}},
	}, "")
}

// stop ends the turn.
func (s *session) stop(at time.Time, summary string) {
	switch s.agent {
	case claude:
		s.hook(at, "Stop", map[string]any{"last_assistant_message": summary, "stop_hook_active": false})
	case codex:
		s.hook(at, "Stop", map[string]any{"last_assistant_message": summary, "stop_hook_active": false})
	case cursor:
		s.hook(at, "stop", map[string]any{"status": "completed", "loop_count": 0})
	case gemini:
		s.hook(at, "AfterAgent", map[string]any{"prompt_response": summary})
	}
}

// fail ends the turn with an error.
func (s *session) fail(at time.Time, msg string) {
	switch s.agent {
	case claude:
		s.hook(at, "StopFailure", map[string]any{"error": msg})
	case cursor:
		s.hook(at, "stop", map[string]any{"status": "error", "loop_count": 0})
	default:
		s.stop(at, msg)
	}
}

// end closes the session.
func (s *session) end(at time.Time) {
	switch s.agent {
	case claude, codex:
		s.hook(at, "SessionEnd", map[string]any{"reason": "prompt_input_exit"})
	case cursor:
		s.hook(at, "sessionEnd", map[string]any{"reason": "completed", "final_status": "completed"})
	case gemini:
		s.hook(at, "SessionEnd", map[string]any{"reason": "exit"})
	}
}
