package geminicli

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/elephaant/shiplino/pkg/adapters"
)

// The conversation is read from the chat recording (see transcript.go).
// Messages are re-appended whole when they change, so each carries its id
// as Key and the last copy wins. user messages are prompts, gemini
// messages are replies plus their tool calls; info, error and warning
// messages are the CLI's own. write_todos (checked against
// packages/core/src/tools/definitions/coreTools.ts on 2026-10-10) carries
// the whole list as {todos: [{description, status}]}.

// ConversationFiles implements adapters.ConversationReader: subagents are
// in chats/<session id>/, named by the main file's first line.
func (Adapter) ConversationFiles(path string) []string {
	out := []string{path}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	r := bufio.NewReader(f)
	first, _ := r.ReadBytes('\n')
	var head record
	if json.Unmarshal(first, &head) != nil || head.SessionID == "" || strings.ContainsAny(head.SessionID, `/\.`) {
		return out
	}
	subs, _ := filepath.Glob(filepath.Join(filepath.Dir(path), head.SessionID, "*.jsonl"))
	return append(out, subs...)
}

// ConversationLine implements adapters.ConversationReader.
func (Adapter) ConversationLine(raw []byte, _ map[string]string) []adapters.Message {
	var r record
	if json.Unmarshal(raw, &r) != nil || r.ID == "" {
		return nil
	}
	ts, _ := time.Parse(time.RFC3339Nano, r.Timestamp)
	switch r.Type {
	case "user":
		if t := text(r.Content); strings.TrimSpace(t) != "" {
			return []adapters.Message{{Role: adapters.RoleUser, Text: t, TS: ts, Key: r.ID}}
		}
	case "gemini":
		var out []adapters.Message
		if t := text(r.Content); strings.TrimSpace(t) != "" {
			out = append(out, adapters.Message{Role: adapters.RoleAssistant, Text: t, TS: ts, Key: r.ID})
		}
		for _, tc := range r.ToolCalls {
			m := adapters.Message{Role: adapters.RoleTool, Tool: tc.Name, Text: summarize(tc.Name, tc.Args), TS: ts, Key: r.ID + ":" + tc.ID}
			if tc.Name == "write_todos" {
				var in struct {
					Todos []struct {
						Description string `json:"description"`
						Status      string `json:"status"`
					} `json:"todos"`
				}
				_ = json.Unmarshal(tc.Args, &in)
				m.Todos = []adapters.Todo{}
				for _, t := range in.Todos {
					m.Todos = append(m.Todos, adapters.Todo{Text: t.Description, Status: t.Status})
				}
			}
			out = append(out, m)
		}
		return out
	}
	return nil
}
