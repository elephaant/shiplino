package claudecode

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/elephaant/shiplino/pkg/adapters"
	"github.com/elephaant/shiplino/pkg/model"
)

// Activity from transcripts, for sessions the hooks didn't see (history
// from before setup, or hooks broken by an update). Each transcript record
// is replayed through the hook parser as the hook payload it corresponds
// to, so the events, their dedup keys and Claude Code's own diff counts are
// exactly what the hooks would have produced:
//
//	user prompt (plain text)         UserPromptSubmit → turn.start
//	assistant tool_use block         PreToolUse       → tool.start
//	user tool_result + toolUseResult PostToolUse(Failure) → tool.end, shell.exec, file.edit, …
//	assistant stop_reason end_turn   Stop             → turn.end
//	pr-link                          git.pr (the agent's own PR record)
//
// Pending tool calls are kept in meta.State until their result arrives.

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
}

type activityLine struct {
	Type        string `json:"type"`
	UUID        string `json:"uuid"`
	SessionID   string `json:"sessionId"`
	AgentID     string `json:"agentId"`
	Timestamp   string `json:"timestamp"`
	CWD         string `json:"cwd"`
	IsMeta      bool   `json:"isMeta"`
	IsSidechain bool   `json:"isSidechain"`
	Message     struct {
		ID         string          `json:"id"`
		Model      string          `json:"model"`
		StopReason string          `json:"stop_reason"`
		Content    json.RawMessage `json:"content"`
	} `json:"message"`
	ToolUseResult json.RawMessage `json:"toolUseResult"`
	PRNumber      int             `json:"prNumber"`
	PRURL         string          `json:"prUrl"`
}

func activity(raw []byte, meta adapters.TranscriptMeta) []model.Event {
	var l activityLine
	if json.Unmarshal(raw, &l) != nil || l.SessionID == "" {
		return nil
	}
	ts := meta.ReceivedAt
	if t, err := time.Parse(time.RFC3339Nano, l.Timestamp); err == nil {
		ts = t
	}
	st := meta.State
	replay := func(envelope string, p map[string]any) []model.Event {
		p["session_id"] = l.SessionID
		if l.CWD != "" {
			p["cwd"] = l.CWD
		}
		if l.AgentID != "" {
			p["agent_id"] = l.AgentID
		}
		if meta.Warmup {
			return nil
		}
		b, _ := json.Marshal(p)
		evs, err := Adapter{}.ParseHook(b, adapters.HookMeta{EnvelopeID: envelope, ReceivedAt: ts, User: meta.User, Ref: meta.Ref})
		if err != nil {
			return nil
		}
		for i := range evs {
			evs[i].Collector = model.CollectorTranscript
			evs[i].ReceivedAt = meta.ReceivedAt.UTC()
		}
		return evs
	}

	switch l.Type {
	case "pr-link":
		if meta.Warmup || l.PRURL == "" {
			return nil
		}
		sid := model.SessionID(Name, l.SessionID)
		return []model.Event{{
			ID: model.NewULID(ts), V: model.SchemaVersion, TS: ts.UTC(), ReceivedAt: meta.ReceivedAt.UTC(),
			Kind: model.KindGitPR, Agent: model.Agent{Name: Name}, Collector: model.CollectorTranscript,
			User: meta.User, SessionID: sid, ActorID: sid,
			Data:     map[string]any{"number": l.PRNumber, "url": l.PRURL, "source": "agent"},
			DedupKey: sid + ":pr:" + strconv.Itoa(l.PRNumber),
		}}

	case "user":
		if l.IsMeta {
			return nil
		}
		var text string
		if json.Unmarshal(l.Message.Content, &text) == nil {
			if prompt(text) && st != nil {
				return replay("prompt-"+l.UUID, map[string]any{"hook_event_name": "UserPromptSubmit", "prompt": text})
			}
			return nil
		}
		var blocks []contentBlock
		if json.Unmarshal(l.Message.Content, &blocks) != nil {
			return nil
		}
		var out []model.Event
		var texts []string
		for _, b := range blocks {
			switch b.Type {
			case "text":
				texts = append(texts, b.Text)
			case "tool_result":
				call, ok := st["tool:"+b.ToolUseID]
				if !ok {
					continue // its tool_use was never seen
				}
				delete(st, "tool:"+b.ToolUseID)
				name, input, _ := strings.Cut(call, "\x00")
				p := map[string]any{"hook_event_name": "PostToolUse", "tool_name": name, "tool_input": json.RawMessage(input),
					"tool_use_id": b.ToolUseID}
				if len(l.ToolUseResult) > 0 && l.ToolUseResult[0] == '{' {
					p["tool_response"] = l.ToolUseResult
				}
				if b.IsError {
					p["hook_event_name"] = "PostToolUseFailure"
					p["error"] = resultText(b.Content)
				}
				out = append(out, replay("result-"+b.ToolUseID, p)...)
			}
		}
		if len(out) == 0 && len(texts) > 0 {
			if t := strings.Join(texts, "\n"); prompt(t) {
				return replay("prompt-"+l.UUID, map[string]any{"hook_event_name": "UserPromptSubmit", "prompt": t})
			}
		}
		return out

	case "assistant":
		var blocks []contentBlock
		_ = json.Unmarshal(l.Message.Content, &blocks)
		var out []model.Event
		for _, b := range blocks {
			switch b.Type {
			case "tool_use":
				if st != nil {
					st["tool:"+b.ID] = b.Name + "\x00" + string(b.Input)
				}
				out = append(out, replay("use-"+b.ID, map[string]any{"hook_event_name": "PreToolUse", "tool_name": b.Name,
					"tool_input": b.Input, "tool_use_id": b.ID})...)
			}
		}
		// A response spans several lines; the turn ends on the one with its
		// final text (thinking-only lines carry the same stop reason).
		var last string
		for _, b := range blocks {
			if b.Type == "text" {
				last = b.Text
			}
		}
		if (l.Message.StopReason == "end_turn" || l.Message.StopReason == "stop_sequence") && l.Message.ID != "" && last != "" {
			out = append(out, replay("stop-"+l.Message.ID, map[string]any{"hook_event_name": "Stop", "last_assistant_message": last})...)
		}
		return out
	}
	return nil
}

// prompt tells a user's prompt from text Claude Code inserts itself
// (command output, task notifications, system reminders), which starts
// with a tag.
func prompt(s string) bool {
	s = strings.TrimSpace(s)
	return s != "" && !strings.HasPrefix(s, "<")
}

// resultText reads a tool_result's content (a string or text blocks).
func resultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []contentBlock
	_ = json.Unmarshal(raw, &blocks)
	var parts []string
	for _, b := range blocks {
		if b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// TranscriptRoots implements adapters.TranscriptDiscoverer: sessions
// started before setup, or whose hooks aren't installed, are found here.
// Subagent transcripts are read alongside their session's.
func (Adapter) TranscriptRoots(userHome string, _, _ time.Time) []string {
	return []string{filepath.Join(userHome, ".claude", "projects", "*", "*.jsonl")}
}
