// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package model

import (
	"errors"
	"fmt"
	"time"
)

// SchemaVersion is the version of the universal event format (schema/event.v1.json).
const SchemaVersion = 1

// Kind is the type of thing that happened.
type Kind string

const (
	KindSessionStart  Kind = "session.start"
	KindSessionEnd    Kind = "session.end"
	KindTurnStart     Kind = "turn.start"
	KindTurnEnd       Kind = "turn.end"
	KindToolStart     Kind = "tool.start"
	KindToolEnd       Kind = "tool.end"
	KindShellExec     Kind = "shell.exec"
	KindFileRead      Kind = "file.read"
	KindFileEdit      Kind = "file.edit"
	KindMCPCall       Kind = "mcp.call"
	KindWaitingStart  Kind = "waiting.start"
	KindWaitingEnd    Kind = "waiting.end"
	KindSubagentStart Kind = "subagent.start"
	KindSubagentEnd   Kind = "subagent.end"
	KindUsage         Kind = "usage"
	KindCompact       Kind = "compact"
	KindGitCommit     Kind = "git.commit"
	KindGitBranch     Kind = "git.branch"
	KindError         Kind = "error"
	KindNote          Kind = "note"
)

var knownKinds = map[Kind]bool{
	KindSessionStart: true, KindSessionEnd: true, KindTurnStart: true, KindTurnEnd: true,
	KindToolStart: true, KindToolEnd: true, KindShellExec: true, KindFileRead: true,
	KindFileEdit: true, KindMCPCall: true, KindWaitingStart: true, KindWaitingEnd: true,
	KindSubagentStart: true, KindSubagentEnd: true, KindUsage: true, KindCompact: true,
	KindGitCommit: true, KindGitBranch: true, KindError: true, KindNote: true,
}

// Known reports whether k is a kind defined by schema v1.
func (k Kind) Known() bool { return knownKinds[k] }

// Collector says where an event came from.
type Collector string

const (
	CollectorHook       Collector = "hook"
	CollectorTranscript Collector = "transcript"
	CollectorGit        Collector = "git"
	CollectorOTLP       Collector = "otlp"
	CollectorHTTP       Collector = "http"
	CollectorWrap       Collector = "wrap"
)

func (c Collector) known() bool {
	switch c {
	case CollectorHook, CollectorTranscript, CollectorGit, CollectorOTLP, CollectorHTTP, CollectorWrap:
		return true
	}
	return false
}

// Normalized tool names used in tool.start data["tool"].
const (
	ToolEdit   = "edit"
	ToolWrite  = "write"
	ToolRead   = "read"
	ToolShell  = "shell"
	ToolSearch = "search"
	ToolWeb    = "web"
	ToolMCP    = "mcp"
	ToolTask   = "task"
	ToolOther  = "other"
)

// Agent identifies the agent product that produced an event.
type Agent struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Surface string `json:"surface,omitempty"` // cli | ide | desktop | cloud
}

// Project describes where the agent was working.
type Project struct {
	ID       string `json:"id,omitempty"`
	CWD      string `json:"cwd,omitempty"`
	RepoRoot string `json:"repo_root,omitempty"`
	Remote   string `json:"remote,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Head     string `json:"head,omitempty"`
}

// RawRef points at the original payload an event was built from.
type RawRef struct {
	Ref string `json:"ref,omitempty"`
}

// Event is one thing an AI agent did, in the universal format.
type Event struct {
	ID          string         `json:"id"`
	V           int            `json:"v"`
	TS          time.Time      `json:"ts"`
	ReceivedAt  time.Time      `json:"received_at,omitzero"`
	Kind        Kind           `json:"kind"`
	Agent       Agent          `json:"agent"`
	Collector   Collector      `json:"collector"`
	MachineID   string         `json:"machine_id,omitempty"`
	User        string         `json:"user,omitempty"`
	SessionID   string         `json:"session_id"`
	ActorID     string         `json:"actor_id,omitempty"`
	ParentActor string         `json:"parent_actor,omitempty"`
	ActorType   string         `json:"actor_type,omitempty"`
	TurnID      string         `json:"turn_id,omitempty"`
	Project     *Project       `json:"project,omitempty"`
	Data        map[string]any `json:"data,omitempty"`
	DedupKey    string         `json:"dedup_key,omitempty"`
	Raw         *RawRef        `json:"raw,omitempty"`
}

// ErrInvalid wraps every validation failure.
var ErrInvalid = errors.New("invalid event")

// Validate checks the fields required by schema v1.
func (e *Event) Validate() error {
	switch {
	case e.ID == "":
		return fmt.Errorf("%w: missing id", ErrInvalid)
	case e.V != SchemaVersion:
		return fmt.Errorf("%w: unsupported version %d", ErrInvalid, e.V)
	case e.TS.IsZero():
		return fmt.Errorf("%w: missing ts", ErrInvalid)
	case !e.Kind.Known():
		return fmt.Errorf("%w: unknown kind %q", ErrInvalid, e.Kind)
	case e.Agent.Name == "":
		return fmt.Errorf("%w: missing agent.name", ErrInvalid)
	case !e.Collector.known():
		return fmt.Errorf("%w: unknown collector %q", ErrInvalid, e.Collector)
	case e.SessionID == "":
		return fmt.Errorf("%w: missing session_id", ErrInvalid)
	}
	return nil
}

// SessionID namespaces an agent's native session id, e.g. "claude-code:3f2c…".
func SessionID(agent, native string) string { return agent + ":" + native }
