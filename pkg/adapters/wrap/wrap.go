// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

// Package wrap parses what `shiplino wrap` records for agents without
// hooks: the run itself (start, end, exit code) and, for Aider, the chat
// history lines appended during the run (see package aider).
//
// `shiplino wrap` writes these to the spool under the agent name "wrap",
// in the same envelope as the hook shim. The payload names the real
// agent, so events carry it (e.g. "aider") and session ids are
// "<agent>:<ulid>".
package wrap

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/elephaant/shiplino/pkg/adapters"
	"github.com/elephaant/shiplino/pkg/adapters/aider"
	"github.com/elephaant/shiplino/pkg/model"
)

// Name is the spool agent name of wrapped runs.
const Name = "wrap"

// Envelope event names.
const (
	EventStart   = "start"
	EventEnd     = "end"
	EventHistory = "aider.history"
)

// Payload is what `shiplino wrap` writes for one envelope.
type Payload struct {
	SessionID string `json:"session_id"` // ULID minted by the wrapper
	Agent     string `json:"agent"`      // "aider", --agent, or "wrap"
	CWD       string `json:"cwd,omitempty"`

	// start
	Command string `json:"command,omitempty"` // redacted command line (standard level and up)
	Title   string `json:"title,omitempty"`
	PID     int    `json:"pid,omitempty"` // the wrapped process

	// end
	ExitCode   *int   `json:"exit_code,omitempty"`
	Signal     string `json:"signal,omitempty"` // set when a signal ended the run
	DurationMS int64  `json:"duration_ms,omitempty"`

	// aider.history
	Root  string       `json:"root,omitempty"` // edit paths are relative to it
	Lines []aider.Line `json:"lines,omitempty"`
}

// Adapter parses wrapped runs.
type Adapter struct{}

func init() { adapters.Register(Adapter{}) }

// Name implements adapters.Adapter.
func (Adapter) Name() string { return Name }

// ParseHook implements adapters.Adapter for wrapper envelopes.
func (Adapter) ParseHook(payload []byte, meta adapters.HookMeta) ([]model.Event, error) {
	var p Payload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, fmt.Errorf("wrap payload: %w", err)
	}
	if p.SessionID == "" {
		return nil, fmt.Errorf("wrap payload: missing session_id")
	}
	if p.Agent == "" {
		p.Agent = Name
	}
	b := builder{p: p, meta: meta, sid: model.SessionID(p.Agent, p.SessionID)}
	switch meta.Event {
	case EventStart:
		return b.start(), nil
	case EventEnd:
		return b.end(), nil
	case EventHistory:
		return b.history(), nil
	}
	return nil, adapters.ErrUnknownEvent
}

type builder struct {
	p    Payload
	meta adapters.HookMeta
	sid  string
}

func (b builder) event(kind model.Kind, key string, data map[string]any) model.Event {
	ts := b.meta.ReceivedAt.UTC()
	e := model.Event{
		ID: model.NewULID(b.meta.ReceivedAt), V: model.SchemaVersion, TS: ts, ReceivedAt: ts,
		Kind: kind, Agent: model.Agent{Name: b.p.Agent, Surface: "cli"}, Collector: model.CollectorWrap,
		MachineID: b.meta.MachineID, User: b.meta.User, SessionID: b.sid, ActorID: b.sid, Data: data,
		DedupKey: b.sid + ":" + key,
	}
	if b.p.CWD != "" {
		e.Project = &model.Project{CWD: b.p.CWD}
	}
	if b.meta.Ref != "" {
		e.Raw = &model.RawRef{Ref: b.meta.Ref}
	}
	return e
}

func (b builder) start() []model.Event {
	d := map[string]any{"wrapped": true}
	if b.p.Command != "" {
		d["command"] = b.p.Command
	}
	if b.p.Title != "" {
		d["title"] = b.p.Title
	}
	if b.p.PID > 0 {
		d["pid"] = b.p.PID
	}
	return []model.Event{b.event(model.KindSessionStart, "start", d)}
}

// end closes the session with the process's own exit code. A non-zero
// exit that wasn't caused by a signal (the user quitting with Ctrl-C)
// fails the session, through a turn.end with status "error".
func (b builder) end() []model.Event {
	d := map[string]any{}
	if b.p.ExitCode != nil {
		d["exit_code"] = *b.p.ExitCode
	}
	if b.p.Signal != "" {
		d["signal"] = b.p.Signal
	}
	if b.p.DurationMS > 0 {
		d["duration_ms"] = b.p.DurationMS
	}
	var out []model.Event
	if b.p.ExitCode != nil && *b.p.ExitCode != 0 && b.p.Signal == "" {
		out = append(out, b.event(model.KindTurnEnd, "exit-error", map[string]any{
			"status": "error", "error": fmt.Sprintf("exited with code %d", *b.p.ExitCode), "exit_code": *b.p.ExitCode,
		}))
	}
	return append(out, b.event(model.KindSessionEnd, "end", d))
}

// history maps Aider's chat history lines. The usage report follows each
// response; edits and the auto commit follow within the same write burst,
// so a batch holding a usage report ends the turn after its last line.
func (b builder) history() []model.Event {
	var out []model.Event
	var turnEnd int64 = -1
	for _, it := range aider.Parse(b.p.Lines) {
		key := fmt.Sprintf("aider:%s:%d", it.Kind, it.Offset)
		switch it.Kind {
		case aider.ItemPrompt:
			out = append(out, b.event(model.KindTurnStart, key, map[string]any{"prompt": it.Text}))
		case aider.ItemVersion:
			e := b.event(model.KindSessionUpdate, key, map[string]any{"agent_version": it.Text})
			e.Agent.Version = it.Text
			out = append(out, e)
		case aider.ItemModel:
			out = append(out, b.event(model.KindSessionUpdate, key, map[string]any{"model": it.Text}))
		case aider.ItemTokens:
			// "sent" counts cached prompt tokens too; the universal event
			// keeps them apart.
			in := max(it.Sent-it.CacheHit-it.CacheWrite, 0)
			out = append(out, b.event(model.KindUsage, key, map[string]any{
				"input_tokens": in, "output_tokens": it.Received,
				"cache_read_tokens": it.CacheHit, "cache_write_tokens": it.CacheWrite,
				"tokens_source": "reported", "tokens_rounded": true,
			}))
			turnEnd = it.Offset
		case aider.ItemCost:
			// The running session total is Aider's own accounting; the
			// engine attributes each increase of it to this session.
			out = append(out, b.event(model.KindUsage, key, map[string]any{
				"report": true, "cost_source": "reported", "process": b.sid,
				"total_cost_usd": it.SessionCost, "message_cost_usd": it.MessageCost,
			}))
			turnEnd = max(turnEnd, it.Offset)
		case aider.ItemEdit:
			path := filepath.FromSlash(it.Text)
			if !filepath.IsAbs(path) && b.p.Root != "" {
				path = filepath.Join(b.p.Root, path)
			}
			out = append(out, b.event(model.KindFileEdit, key, map[string]any{"path": path, "tool": model.ToolEdit}))
		case aider.ItemCommit:
			// Aider runs git commit itself (auto commits): this links the
			// commit to the session exactly.
			d := map[string]any{"command": "git commit", "exit_code": 0, "sha": it.SHA, "tool": model.ToolShell}
			if it.Text != "" {
				d["commit_message"] = it.Text
			}
			out = append(out, b.event(model.KindShellExec, key, d))
		}
	}
	if turnEnd >= 0 {
		out = append(out, b.event(model.KindTurnEnd, fmt.Sprintf("aider:turn-end:%d", turnEnd), map[string]any{}))
	}
	return out
}
