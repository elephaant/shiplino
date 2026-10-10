package cursor

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"github.com/elephaant/shiplino/pkg/adapters"
)

// The conversation is read from the agent transcript (see transcript.go):
// the <user_query> of user lines, assistant text blocks, and tool_use
// blocks. Lines have no timestamps; a user line's <timestamp> (minute
// precision) is used when present. TodoWrite with "merge" updates the
// list by item id, so the whole list is kept in state.

// ConversationFiles implements adapters.ConversationReader.
func (Adapter) ConversationFiles(path string) []string {
	if _, _, sub := subagentPath(path); sub {
		return []string{path}
	}
	subs, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "subagents", "*.jsonl"))
	return append([]string{path}, subs...)
}

// ConversationLine implements adapters.ConversationReader.
func (Adapter) ConversationLine(raw []byte, st map[string]string) []adapters.Message {
	var l transcriptLine
	if json.Unmarshal(raw, &l) != nil {
		return nil
	}
	var ts time.Time
	switch l.Role {
	case "user":
		for _, c := range l.Message.Content {
			if m := stampRE.FindStringSubmatch(c.Text); m != nil {
				if t, ok := parseStamp(m[1]); ok {
					ts = t
				}
			}
			if m := queryRE.FindStringSubmatch(c.Text); m != nil && strings.TrimSpace(m[1]) != "" {
				return []adapters.Message{{Role: adapters.RoleUser, Text: m[1], TS: ts}}
			}
		}
	case "assistant":
		var out []adapters.Message
		for _, c := range l.Message.Content {
			switch {
			case c.Type == "text" && strings.TrimSpace(c.Text) != "":
				out = append(out, adapters.Message{Role: adapters.RoleAssistant, Text: c.Text})
			case c.Type == "tool_use" && c.Name != "":
				m := adapters.Message{Role: adapters.RoleTool, Tool: c.Name, Text: summarize(c.Name, c.Input)}
				if c.Name == "TodoWrite" {
					m.Todos = mergeTodos(st, c.Input)
				}
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}

// mergeTodos applies a TodoWrite call to the list kept in st and returns
// the whole list.
func mergeTodos(st map[string]string, raw json.RawMessage) []adapters.Todo {
	type item struct {
		ID      string `json:"id"`
		Content string `json:"content"`
		Status  string `json:"status"`
	}
	var in struct {
		Merge bool   `json:"merge"`
		Todos []item `json:"todos"`
	}
	_ = json.Unmarshal(toolInputRaw(raw), &in)
	var list []item
	if in.Merge && st != nil {
		_ = json.Unmarshal([]byte(st["todos"]), &list)
	}
	for _, t := range in.Todos {
		found := false
		for i := range list {
			if t.ID != "" && list[i].ID == t.ID {
				if t.Content != "" {
					list[i].Content = t.Content
				}
				if t.Status != "" {
					list[i].Status = t.Status
				}
				found = true
				break
			}
		}
		if !found {
			list = append(list, t)
		}
	}
	if st != nil {
		b, _ := json.Marshal(list)
		st["todos"] = string(b)
	}
	out := []adapters.Todo{}
	for _, t := range list {
		out = append(out, adapters.Todo{Text: t.Content, Status: t.Status})
	}
	return out
}

// toolInputRaw unwraps input that Cursor sometimes writes as a JSON string.
func toolInputRaw(raw json.RawMessage) json.RawMessage {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return json.RawMessage(s)
	}
	return raw
}
