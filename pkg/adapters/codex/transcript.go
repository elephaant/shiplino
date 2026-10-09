// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package codex

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/elephaant/shiplino/pkg/adapters"
	"github.com/elephaant/shiplino/pkg/model"
	"github.com/elephaant/shiplino/pkg/pricing"
)

// Rollout format, checked against local Codex rollouts (CLI 0.153) on
// 2026-10-10: ~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl, one JSON
// object per line: {timestamp, type, payload}.
//
//	session_meta            session id, cwd, cli_version, git branch
//	turn_context            model and cwd for the following turn
//	event_msg/task_started  a turn begins (turn_id)
//	event_msg/user_message  the prompt (imported "legacy" history)
//	item_completed/UserMessage the prompt (current format)
//	event_msg/thread_settings_applied model changes
//	event_msg/task_complete turn end (duration_ms)
//	event_msg/turn_aborted  turn interrupted
//	token_usage_record      Codex's own usage per API response (response_id)
//	event_msg/item_completed CommandExecution, FileChange, McpToolCall
//
// token_usage_record is used for tokens: one per API response, so each is
// a billed call. Summed over unique response ids they match Codex's own
// thread_token_usage, except after a rewind: the thread total then drops
// the abandoned branch, whose calls were still made. The running total in
// token_count resets within a session, so it isn't used.

type line struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type usage struct {
	Input      int64 `json:"input_tokens"`
	Cached     int64 `json:"cached_input_tokens"`
	CacheWrite int64 `json:"cache_write_input_tokens"`
	Output     int64 `json:"output_tokens"`
	Reasoning  int64 `json:"reasoning_output_tokens"`
}

// ParseTranscriptLine implements adapters.TranscriptParser. It keeps the
// session id, model and current turn in meta.State.
func (Adapter) ParseTranscriptLine(raw []byte, meta adapters.TranscriptMeta) ([]model.Event, error) {
	var l line
	if err := json.Unmarshal(raw, &l); err != nil {
		return nil, fmt.Errorf("codex rollout: %w", err)
	}
	st := meta.State
	if st == nil {
		st = map[string]string{}
	}
	var p map[string]json.RawMessage
	_ = json.Unmarshal(l.Payload, &p)
	sub := str(p["type"])

	switch {
	case l.Type == "session_meta":
		var m struct {
			SessionID string          `json:"session_id"`
			ID        string          `json:"id"`
			CWD       string          `json:"cwd"`
			Version   string          `json:"cli_version"`
			Source    json.RawMessage `json:"source"`
			Base      struct {
				Provenance struct {
					Model string `json:"model"`
				} `json:"provenance"`
			} `json:"base_instructions"`
			Git struct {
				Branch string `json:"branch"`
			} `json:"git"`
		}
		_ = json.Unmarshal(l.Payload, &m)
		st["session"] = first(m.SessionID, m.ID)
		st["cwd"], st["version"], st["branch"] = m.CWD, m.Version, m.Git.Branch
		if st["model"] == "" {
			st["model"] = m.Base.Provenance.Model // until a turn names its model
		}
		// Internal helper threads (e.g. the approvals reviewer) aren't user sessions.
		if strings.Contains(string(m.Source), `"subagent"`) {
			st["skip"] = "1"
		}
		if meta.Warmup || st["skip"] == "1" || st["session"] == "" {
			return nil, nil
		}
		e := event(st, meta, l, model.KindSessionStart, map[string]any{"source": "rollout"}, "session.start")
		return []model.Event{e}, nil
	case l.Type == "turn_context":
		var c struct {
			Model string `json:"model"`
			CWD   string `json:"cwd"`
		}
		_ = json.Unmarshal(l.Payload, &c)
		if c.Model != "" {
			st["model"] = c.Model
		}
		if c.CWD != "" {
			st["cwd"] = c.CWD
		}
		return nil, nil
	}
	if sub == "thread_settings_applied" {
		var t struct {
			Settings struct {
				Model string `json:"model"`
			} `json:"thread_settings"`
		}
		_ = json.Unmarshal(l.Payload, &t)
		if t.Settings.Model != "" {
			st["model"] = t.Settings.Model
		}
		return nil, nil
	}
	if sub == "task_started" {
		st["turn"] = str(p["turn_id"])
		return nil, nil
	}
	if meta.Warmup || st["skip"] == "1" || st["session"] == "" {
		return nil, nil
	}

	switch {
	case l.Type == "token_usage_record":
		var r struct {
			ResponseID string `json:"response_id"`
			TurnID     string `json:"turn_id"`
			Usage      usage  `json:"usage"`
		}
		if json.Unmarshal(l.Payload, &r) != nil || r.ResponseID == "" {
			return nil, nil
		}
		u := r.Usage
		// OpenAI counts cached tokens inside input_tokens.
		uncached := u.Input - u.Cached - u.CacheWrite
		if uncached < 0 {
			uncached = 0
		}
		data := map[string]any{
			"model": st["model"], "message_id": r.ResponseID, "input_tokens": uncached, "output_tokens": u.Output,
			"cache_read_tokens": u.Cached, "cache_write_tokens": u.CacheWrite, "reasoning_tokens": u.Reasoning,
		}
		if cost, ok := pricing.Default().Cost(st["model"], pricing.Usage{Input: uncached, Output: u.Output, CacheRead: u.Cached, CacheWrite5m: u.CacheWrite}); ok {
			data["cost_usd"], data["cost_source"] = cost, "computed"
		} else {
			data["cost_source"] = "unpriced"
		}
		e := event(st, meta, l, model.KindUsage, data, "usage:"+r.ResponseID)
		e.TurnID = r.TurnID
		return []model.Event{e}, nil
	case sub == "user_message":
		return []model.Event{turnStart(st, meta, l, str(p["message"]), st["turn"])}, nil
	case sub == "task_complete":
		var c struct {
			TurnID     string `json:"turn_id"`
			Last       string `json:"last_agent_message"`
			DurationMS int64  `json:"duration_ms"`
		}
		_ = json.Unmarshal(l.Payload, &c)
		e := event(st, meta, l, model.KindTurnEnd, compactMap(map[string]any{"status": "ok", "assistant_summary": c.Last, "duration_ms": c.DurationMS}), "turnend:"+first(c.TurnID, st["turn"]))
		return []model.Event{e}, nil
	case sub == "turn_aborted":
		e := event(st, meta, l, model.KindTurnEnd, map[string]any{"status": "interrupted"}, "turnend:"+first(str(p["turn_id"]), st["turn"]))
		return []model.Event{e}, nil
	case sub == "item_completed":
		var um struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(p["item"], &um) == nil && um.Type == "UserMessage" {
			var parts []string
			for _, c := range um.Content {
				if c.Text != "" {
					parts = append(parts, c.Text)
				}
			}
			return []model.Event{turnStart(st, meta, l, strings.Join(parts, "\n"), first(str(p["turn_id"]), st["turn"]))}, nil
		}
		return items(st, meta, l, p["item"]), nil
	}
	return nil, nil
}

func turnStart(st map[string]string, meta adapters.TranscriptMeta, l line, prompt, turn string) model.Event {
	return event(st, meta, l, model.KindTurnStart, map[string]any{"prompt": prompt, "prompt_chars": len([]rune(prompt)), "model": st["model"]}, "turn:"+turn)
}

// items turns completed commands, file changes and MCP calls into events.
func items(st map[string]string, meta adapters.TranscriptMeta, l line, raw json.RawMessage) []model.Event {
	var it struct {
		Type     string          `json:"type"`
		ID       string          `json:"id"`
		Command  json.RawMessage `json:"command"`
		CWD      string          `json:"cwd"`
		ExitCode *int            `json:"exit_code"`
		Duration struct {
			Secs  int64 `json:"secs"`
			Nanos int64 `json:"nanos"`
		} `json:"duration"`
		Changes map[string]struct {
			Type        string `json:"type"`
			Content     string `json:"content"`
			UnifiedDiff string `json:"unified_diff"`
		} `json:"changes"`
		Server string `json:"server"`
		Tool   string `json:"tool"`
		Status string `json:"status"`
	}
	if json.Unmarshal(raw, &it) != nil || it.ID == "" {
		return nil
	}
	ms := it.Duration.Secs*1000 + it.Duration.Nanos/1e6
	key := "item:" + it.ID
	pair := func(tool, raw, summary string, ok bool) []model.Event {
		start := event(st, meta, l, model.KindToolStart, map[string]any{"tool_call_id": it.ID, "tool": tool, "tool_raw": raw, "input_summary": summary}, key+":start")
		end := event(st, meta, l, model.KindToolEnd, map[string]any{"tool_call_id": it.ID, "tool": tool, "ok": ok, "duration_ms": ms}, key+":end")
		return []model.Event{start, end}
	}
	switch it.Type {
	case "CommandExecution":
		cmd := commandText(json.RawMessage(`{"command":` + string(nonNull(it.Command)) + `}`))
		ok := it.ExitCode == nil || *it.ExitCode == 0
		out := pair(model.ToolShell, "exec", cmd, ok)
		d := map[string]any{"command": cmd, "tool_call_id": it.ID, "duration_ms": ms, "cwd": first(it.CWD, st["cwd"])}
		if it.ExitCode != nil {
			d["exit_code"] = *it.ExitCode
		}
		return append(out, event(st, meta, l, model.KindShellExec, d, key+":shell"))
	case "FileChange":
		var paths []string
		for p := range it.Changes {
			paths = append(paths, p)
		}
		sort.Strings(paths) // stable event keys
		out := pair(model.ToolEdit, "apply_patch", strings.Join(paths, ", "), it.Status == "" || it.Status == "completed")
		for i, path := range paths {
			ch := it.Changes[path]
			op := map[string]string{"add": "create", "update": "modify", "delete": "delete"}[ch.Type]
			if op == "" {
				op = "modify"
			}
			added, removed := diffLines(ch.UnifiedDiff)
			if ch.Type == "add" {
				added, removed = countLines(ch.Content), 0
			}
			if st["cwd"] != "" {
				path = joinPath(st["cwd"], path)
			}
			out = append(out, event(st, meta, l, model.KindFileEdit, map[string]any{
				"path": path, "op": op, "lines_added": added, "lines_removed": removed, "lines_source": "agent",
			}, fmt.Sprintf("%s:file%d", key, i)))
		}
		return out
	case "McpToolCall":
		ok := it.Status == "" || it.Status == "completed"
		out := pair(model.ToolMCP, it.Server+"__"+it.Tool, it.Server+"/"+it.Tool, ok)
		return append(out, event(st, meta, l, model.KindMCPCall, map[string]any{"server": it.Server, "tool": it.Tool, "ok": ok, "duration_ms": ms}, key+":mcp"))
	}
	return nil
}

// event builds a transcript event with a dedup key that is stable across
// re-reads and shared with hook events where both describe the same thing.
func event(st map[string]string, meta adapters.TranscriptMeta, l line, kind model.Kind, data map[string]any, key string) model.Event {
	ts := meta.ReceivedAt
	if t, err := time.Parse(time.RFC3339Nano, l.Timestamp); err == nil {
		ts = t
	}
	sid := model.SessionID(Name, st["session"])
	e := model.Event{
		ID: model.NewULID(ts), V: model.SchemaVersion, TS: ts.UTC(), ReceivedAt: meta.ReceivedAt.UTC(),
		Kind: kind, Agent: model.Agent{Name: Name, Version: st["version"]}, Collector: model.CollectorTranscript,
		User: meta.User, SessionID: sid, ActorID: sid, TurnID: st["turn"], Data: data, DedupKey: sid + ":" + key,
	}
	if st["cwd"] != "" {
		e.Project = &model.Project{CWD: st["cwd"], Branch: st["branch"]}
	}
	if meta.Ref != "" {
		e.Raw = &model.RawRef{Ref: meta.Ref}
	}
	return e
}

// TranscriptRoots implements adapters.TranscriptDiscoverer: rollouts of
// the last two days (sessions without hooks, e.g. the Codex desktop app).
func (Adapter) TranscriptRoots(userHome string, now time.Time) []string {
	base := filepath.Join(userHome, ".codex", "sessions")
	if _, err := os.Stat(base); err != nil {
		return nil
	}
	var globs []string
	seen := map[string]bool{}
	for _, t := range []time.Time{now, now.UTC(), now.Add(-24 * time.Hour), now.UTC().Add(-24 * time.Hour)} {
		g := filepath.Join(base, t.Format("2006"), t.Format("01"), t.Format("02"), "rollout-*.jsonl")
		if !seen[g] {
			seen[g] = true
			globs = append(globs, g)
		}
	}
	return globs
}

func diffLines(d string) (added, removed int) {
	for _, l := range strings.Split(d, "\n") {
		switch {
		case strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"):
		case strings.HasPrefix(l, "+"):
			added++
		case strings.HasPrefix(l, "-"):
			removed++
		}
	}
	return
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

func str(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

func first(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

func nonNull(b json.RawMessage) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage(`""`)
	}
	return b
}

func compactMap(m map[string]any) map[string]any {
	for k, v := range m {
		switch x := v.(type) {
		case string:
			if x == "" {
				delete(m, k)
			}
		case int64:
			if x == 0 {
				delete(m, k)
			}
		}
	}
	return m
}
