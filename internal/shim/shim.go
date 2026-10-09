// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

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

// Envelope is one spool line: the raw hook payload plus what the shim knows.
// Exactly one of P (inline JSON payload), S (non-JSON payload as text) or
// B (path of a blob file, relative to the spool root) is set.
type Envelope struct {
	ID    string          `json:"id"`
	Agent string          `json:"a"`
	Event string          `json:"e,omitempty"`
	TS    int64           `json:"t"` // Unix nanoseconds when the hook ran
	PID   int             `json:"pid"`
	P     json.RawMessage `json:"p,omitempty"`
	S     string          `json:"s,omitempty"`
	B     string          `json:"b,omitempty"`
}

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
	if _, err := os.Stat(filepath.Join(home, "paused")); err == nil {
		return
	}

	in, _ := io.ReadAll(io.LimitReader(stdin, MaxPayload))
	now := time.Now()
	env := Envelope{ID: model.NewULID(now), Agent: agent, Event: event, TS: now.UnixNano(), PID: os.Getpid()}

	session := ""
	if json.Valid(in) {
		var probe struct {
			SessionID      string `json:"session_id"`
			ConversationID string `json:"conversation_id"`
			HookEventName  string `json:"hook_event_name"`
		}
		_ = json.Unmarshal(in, &probe)
		session = firstNonEmpty(probe.SessionID, probe.ConversationID)
		if env.Event == "" {
			env.Event = probe.HookEventName
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

func marshalLine(env Envelope) ([]byte, error) {
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
