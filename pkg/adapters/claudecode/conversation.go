package claudecode

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"github.com/elephaant/shiplino/pkg/adapters"
)

// The conversation is read from the same transcript lines as activity.go:
// plain-text user lines are prompts (Claude Code's own inserts, meta lines
// and compaction summaries are left out), assistant text blocks are
// replies, and tool_use blocks are tool calls. Tool results aren't shown.
// TodoWrite calls carry the whole todo list. Subagents write their own
// files in <session>/subagents/.

// ConversationFiles implements adapters.ConversationReader.
func (Adapter) ConversationFiles(path string) []string {
	subs, _ := filepath.Glob(filepath.Join(strings.TrimSuffix(path, ".jsonl"), "subagents", "*.jsonl"))
	return append([]string{path}, subs...)
}

type conversationLine struct {
	activityLine
	IsCompactSummary          bool `json:"isCompactSummary"`
	IsVisibleInTranscriptOnly bool `json:"isVisibleInTranscriptOnly"`
}

// ConversationLine implements adapters.ConversationReader.
func (Adapter) ConversationLine(raw []byte, _ map[string]string) []adapters.Message {
	var l conversationLine
	if json.Unmarshal(raw, &l) != nil || l.IsMeta || l.IsCompactSummary || l.IsVisibleInTranscriptOnly {
		return nil
	}
	ts, _ := time.Parse(time.RFC3339Nano, l.Timestamp)
	msg := func(role, text string) adapters.Message {
		return adapters.Message{Role: role, Text: text, TS: ts, Subagent: l.AgentID}
	}
	switch l.Type {
	case "user":
		var text string
		if json.Unmarshal(l.Message.Content, &text) != nil {
			var blocks []contentBlock
			_ = json.Unmarshal(l.Message.Content, &blocks)
			var parts []string
			for _, b := range blocks {
				if b.Type == "text" {
					parts = append(parts, b.Text)
				}
			}
			text = strings.Join(parts, "\n")
		}
		if prompt(text) {
			return []adapters.Message{msg(adapters.RoleUser, text)}
		}
	case "assistant":
		var blocks []contentBlock
		_ = json.Unmarshal(l.Message.Content, &blocks)
		var out []adapters.Message
		for _, b := range blocks {
			switch b.Type {
			case "text":
				if strings.TrimSpace(b.Text) != "" {
					out = append(out, msg(adapters.RoleAssistant, b.Text))
				}
			case "tool_use":
				m := msg(adapters.RoleTool, summarize(b.Name, b.Input))
				m.Tool = b.Name
				if b.Name == "TodoWrite" {
					m.Todos = todos(b.Input)
				}
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}

// todos reads TodoWrite's input: {"todos": [{"content", "status"}]}.
func todos(raw json.RawMessage) []adapters.Todo {
	var in struct {
		Todos []struct {
			Content string `json:"content"`
			Status  string `json:"status"`
		} `json:"todos"`
	}
	_ = json.Unmarshal(raw, &in)
	out := []adapters.Todo{}
	for _, t := range in.Todos {
		out = append(out, adapters.Todo{Text: t.Content, Status: t.Status})
	}
	return out
}
