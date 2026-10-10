// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

package shim

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/elephaant/shiplino/internal/spool"
	"github.com/elephaant/shiplino/pkg/model"
)

// MaxPayload caps how much of stdin the shim reads.
const MaxPayload = 8 << 20

// Run is the whole hook fast path. It must never write to stdout or stderr,
// never block the agent and never fail visibly: every error is swallowed.
func Run(args []string, stdin io.Reader) {
	defer func() { _ = recover() }()

	agent, event := parseFlags(args)
	if agent == "" {
		agent = "unknown"
	}
	home := spool.Home()
	if home == "" {
		return
	}
	if spool.Paused(home, time.Now()) {
		return
	}

	in, _ := io.ReadAll(io.LimitReader(stdin, MaxPayload))
	now := time.Now()
	env := spool.Envelope{ID: model.NewULID(now), Agent: agent, Event: event, TS: now.UnixNano(), PID: os.Getpid()}

	session := ""
	if json.Valid(in) {
		var probe struct {
			SessionID      string `json:"session_id"`
			ConversationID string `json:"conversation_id"`
			TrajectoryID   string `json:"trajectory_id"` // Windsurf
			SessionIDCamel string `json:"sessionId"`     // Copilot CLI
			HookEventName  string `json:"hook_event_name"`
			ActionName     string `json:"agent_action_name"` // Windsurf
		}
		_ = json.Unmarshal(in, &probe)
		session = firstNonEmpty(probe.SessionID, probe.ConversationID, probe.TrajectoryID, probe.SessionIDCamel)
		if env.Event == "" {
			env.Event = firstNonEmpty(probe.HookEventName, probe.ActionName)
		}
		if _, err := os.Stat(filepath.Join(home, spool.MinimalMarker)); err == nil {
			in = stripContent(in)
		}
		env.P = in
	} else if len(in) > 0 {
		env.S = string(in)
	}

	root := spool.Dir(home)
	line, err := marshalLine(env)
	if err != nil {
		return
	}
	if len(line) > spool.MaxLine {
		rel, err := spool.WriteBlob(root, env.ID, in)
		if err != nil {
			return
		}
		env.P, env.S, env.B = nil, "", rel
		if line, err = marshalLine(env); err != nil {
			return
		}
	}
	_ = spool.AppendLine(root, agent, session, line)
}

func marshalLine(env spool.Envelope) ([]byte, error) {
	b, err := json.Marshal(env)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// parseFlags reads --agent and --event (as "--flag value" or "--flag=value").
// It ignores anything else rather than failing.
func parseFlags(args []string) (agent, event string) {
	for i := 0; i < len(args); i++ {
		name, val, hasVal := strings.Cut(args[i], "=")
		if !hasVal && i+1 < len(args) {
			val = args[i+1]
		}
		switch name {
		case "--agent", "-agent":
			agent = val
		case "--event", "-event":
			event = val
		default:
			continue
		}
		if !hasVal {
			i++
		}
	}
	return agent, event
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

// contentKeys hold prompts, outputs and messages: dropped at minimal
// capture level before the payload is written to disk.
var contentKeys = []string{"prompt", "tool_response", "last_assistant_message", "custom_instructions",
	"compact_summary", "message", "title", "error", "error_details", "session_title",
	// Cursor
	"tool_output", "output", "result_json", "text", "content", "edits", "attachments", "command",
	"agent_message", "summary", "task", "description", "error_message", "user_email", "modified_files",
	// Copilot CLI (camelCase payloads)
	"toolResult", "response", "initialPrompt", "transformedPrompt", "customInstructions", "agentDescription",
	// Gemini CLI
	"prompt_response", "details", "llm_request", "llm_response",
	// OpenCode (Shiplino's plugin)
	"diff", "file_patches", "patterns"}

// keepInput are tool_input fields allowed at minimal (file paths only).
var keepInput = map[string]bool{"file_path": true, "notebook_path": true, "path": true, "dir_path": true, "filePath": true}

// keepInfo are Windsurf tool_info fields allowed at minimal: paths and MCP
// names, never the prompt, response, edits, command or MCP arguments.
var keepInfo = map[string]bool{"file_path": true, "cwd": true, "mcp_server_name": true, "mcp_tool_name": true}

// stripContent removes content fields from a JSON object payload. On any
// parse problem it returns {} rather than risk writing content.
func stripContent(in []byte) []byte {
	var m map[string]json.RawMessage
	if json.Unmarshal(in, &m) != nil {
		return []byte("{}")
	}
	for _, k := range contentKeys {
		delete(m, k)
	}
	// Tool arguments, as an object or a JSON string (Copilot's toolArgs):
	// only the fields in each key's keep list stay.
	for key, keep := range map[string]map[string]bool{"tool_input": keepInput, "toolArgs": keepInput, "tool_info": keepInfo} {
		raw, ok := m[key]
		if !ok {
			continue
		}
		var s string
		if json.Unmarshal(raw, &s) == nil {
			raw = []byte(s)
		}
		var ti map[string]json.RawMessage
		if json.Unmarshal(raw, &ti) != nil {
			delete(m, key)
			continue
		}
		for k := range ti {
			if !keep[k] {
				delete(ti, k)
			}
		}
		b, _ := json.Marshal(ti)
		m[key] = b
	}
	out, err := json.Marshal(m)
	if err != nil {
		return []byte("{}")
	}
	return out
}
