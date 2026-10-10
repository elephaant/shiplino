package adapters

import "time"

// Roles of a conversation message.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// Message is one entry of a conversation, read back from the agent's own
// transcript when someone asks for it. Messages are never stored or synced.
type Message struct {
	Role string `json:"role"` // RoleUser, RoleAssistant or RoleTool
	// Text is the prompt or reply, or for a tool call the same short
	// summary hooks record (a command, a path), never file contents.
	Text     string    `json:"text,omitempty"`
	Tool     string    `json:"tool,omitempty"` // the agent's own tool name
	TS       time.Time `json:"ts,omitzero"`
	Subagent string    `json:"subagent,omitempty"` // the subagent's id, if one wrote it
	// Todos is the whole list a todo or plan tool call set.
	Todos []Todo `json:"todos,omitempty"`
	// Key identifies a message the agent rewrites whole: a later message
	// with the same key replaces the earlier one in place.
	Key string `json:"-"`
}

// Todo is one item of an agent's todo list or plan.
type Todo struct {
	Text   string `json:"text"`
	Status string `json:"status,omitempty"` // the agent's own word: pending, in_progress, completed, …
}

// ConversationReader is implemented by adapters that can read a session's
// conversation back from their transcript files, on demand.
type ConversationReader interface {
	// ConversationFiles returns the files that belong to the conversation
	// whose main transcript is path: path first, then subagents' files.
	// Callers check every file against TranscriptRoots before reading it.
	ConversationFiles(path string) []string
	// ConversationLine converts one transcript line into messages. state
	// is memory for one file, in line order.
	ConversationLine(line []byte, state map[string]string) []Message
}
