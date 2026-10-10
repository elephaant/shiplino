package codex

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/elephaant/shiplino/pkg/adapters"
)

// The conversation is read from the rollout (see transcript.go), checked
// against local rollouts on 2026-10-10. Each message is written several
// ways; one record kind per role is used:
//
//	item_completed/UserMessage, or event_msg/user_message (older)  prompts
//	item_completed/AgentMessage, or event_msg/agent_message (older) replies
//	item_completed/CommandExecution, FileChange, McpToolCall       tool calls
//	response_item/function_call update_plan                        the plan
//
// response_item messages are skipped: they repeat the above and add
// injected context (instructions, environment) that isn't the user's.

// ConversationFiles implements adapters.ConversationReader. Subagents run
// as their own threads, in their own rollouts and sessions.
func (Adapter) ConversationFiles(path string) []string { return []string{path} }

// ConversationLine implements adapters.ConversationReader.
func (Adapter) ConversationLine(raw []byte, _ map[string]string) []adapters.Message {
	var l line
	if json.Unmarshal(raw, &l) != nil {
		return nil
	}
	var p map[string]json.RawMessage
	_ = json.Unmarshal(l.Payload, &p)
	ts, _ := time.Parse(time.RFC3339Nano, l.Timestamp)
	one := func(role, text string) []adapters.Message {
		if strings.TrimSpace(text) == "" {
			return nil
		}
		return []adapters.Message{{Role: role, Text: text, TS: ts}}
	}
	switch sub := str(p["type"]); {
	case l.Type == "event_msg" && sub == "user_message":
		return one(adapters.RoleUser, str(p["message"]))
	case l.Type == "event_msg" && sub == "agent_message":
		return one(adapters.RoleAssistant, str(p["message"]))
	case l.Type == "event_msg" && sub == "item_completed":
		var it struct {
			Type    string `json:"type"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			Command json.RawMessage `json:"command"`
			Changes map[string]any  `json:"changes"`
			Server  string          `json:"server"`
			Tool    string          `json:"tool"`
		}
		if json.Unmarshal(p["item"], &it) != nil {
			return nil
		}
		var parts []string
		for _, c := range it.Content {
			parts = append(parts, c.Text)
		}
		switch it.Type {
		case "UserMessage":
			return one(adapters.RoleUser, strings.Join(parts, "\n"))
		case "AgentMessage":
			return one(adapters.RoleAssistant, strings.Join(parts, "\n"))
		case "CommandExecution":
			cmd := summarize("exec", json.RawMessage(`{"command":`+string(nonNull(it.Command))+`}`))
			return []adapters.Message{{Role: adapters.RoleTool, Tool: "exec", Text: cmd, TS: ts}}
		case "FileChange":
			var paths []string
			for p := range it.Changes {
				paths = append(paths, p)
			}
			sort.Strings(paths)
			return []adapters.Message{{Role: adapters.RoleTool, Tool: "apply_patch", Text: strings.Join(paths, ", "), TS: ts}}
		case "McpToolCall":
			return []adapters.Message{{Role: adapters.RoleTool, Tool: it.Server + "__" + it.Tool, Text: it.Server + "/" + it.Tool, TS: ts}}
		}
	case l.Type == "response_item" && sub == "function_call" && str(p["name"]) == "update_plan":
		var args struct {
			Plan []struct {
				Step   string `json:"step"`
				Status string `json:"status"`
			} `json:"plan"`
		}
		_ = json.Unmarshal([]byte(str(p["arguments"])), &args)
		m := adapters.Message{Role: adapters.RoleTool, Tool: "update_plan", Text: "update_plan", TS: ts, Todos: []adapters.Todo{}}
		for _, s := range args.Plan {
			m.Todos = append(m.Todos, adapters.Todo{Text: s.Step, Status: s.Status})
		}
		return []adapters.Message{m}
	}
	return nil
}
