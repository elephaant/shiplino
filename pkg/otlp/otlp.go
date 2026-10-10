// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package otlp

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/elephaant/shiplino/pkg/adapters/claudecode"
	"github.com/elephaant/shiplino/pkg/model"
)

// Claude Code's OpenTelemetry export, checked against its official
// monitoring docs on 2026-10-10. Each event is a log record named
// claude_code.<name> (event.name holds <name>) with session.id,
// app.version, prompt.id, event.timestamp and event.sequence. Mapped:
//
//	claude_code.api_request  → usage (model, cost_usd, input_tokens,
//	                           output_tokens, cache_read_tokens,
//	                           cache_creation_tokens, duration_ms, request_id)
//	claude_code.user_prompt  → turn.start (prompt_length; the prompt only
//	                           when the user enabled OTEL_LOG_USER_PROMPTS)
//	claude_code.tool_result  → tool.start + tool.end (tool_use_id,
//	                           tool_name, success, duration_ms)
//
// Other claude_code.* events (api_error, tool_decision, …) and Codex's
// codex.* events are recognized and counted as ignored: hooks and
// transcripts already record what they say. Metrics and traces aren't
// decoded at all; the receiver only counts those requests.
//
// Usage records are the agent's own per-request accounting. They describe
// the same API requests as transcript usage, so the engine keeps them
// apart (Session.Telemetry) and compares rather than adds them; see
// docs/ingest.md.

// Stats counts what one logs export contained.
type Stats struct {
	Records int `json:"records"` // log records
	Events  int `json:"events"`  // events produced
	Ignored int `json:"ignored"` // known agent records that add nothing hooks and transcripts lack
	Unknown int `json:"unknown"` // records from unknown sources, or without a session id
}

// Add accumulates o into s.
func (s *Stats) Add(o Stats) {
	s.Records += o.Records
	s.Events += o.Events
	s.Ignored += o.Ignored
	s.Unknown += o.Unknown
}

// Meta is what the receiver knows about an export.
type Meta struct {
	ReceivedAt time.Time
	User       string
}

const (
	claudePrefix = "claude_code."
	codexPrefix  = "codex."
)

// Map turns the log records of an export into events.
func Map(ld Logs, meta Meta) ([]model.Event, Stats) {
	var (
		out []model.Event
		st  Stats
	)
	for _, rl := range ld.Resources {
		for _, sl := range rl.Scopes {
			for _, lr := range sl.Records {
				st.Records++
				name := eventName(rl.Attributes, sl.Name, lr)
				switch {
				case strings.HasPrefix(name, claudePrefix):
					evs, ok := claudeCode(strings.TrimPrefix(name, claudePrefix), rl.Attributes, lr, meta)
					switch {
					case !ok:
						st.Unknown++
					case len(evs) == 0:
						st.Ignored++
					}
					st.Events += len(evs)
					out = append(out, evs...)
				case strings.HasPrefix(name, codexPrefix):
					st.Ignored++
				default:
					st.Unknown++
				}
			}
		}
	}
	return out, st
}

// eventName finds a record's full event name. Agents put it in different
// places: the record's event name, its body, or the event.name attribute,
// which Claude Code sets without the claude_code. prefix.
func eventName(res map[string]any, scope string, lr LogRecord) string {
	body, _ := lr.Body.(string)
	var bare string
	for _, n := range []string{lr.EventName, body, attr(lr.Attributes, "event.name")} {
		if strings.HasPrefix(n, claudePrefix) || strings.HasPrefix(n, codexPrefix) {
			return n
		}
		if bare == "" && n != "" && !strings.ContainsAny(n, ". ") {
			bare = n
		}
	}
	if bare != "" && (attr(res, "service.name") == claudecode.Name || strings.Contains(scope, "claude_code")) {
		return claudePrefix + bare
	}
	return bare
}

// claudeCode maps one Claude Code event. ok is false when the record
// can't be attributed to a session.
func claudeCode(name string, res map[string]any, lr LogRecord, meta Meta) (events []model.Event, ok bool) {
	a := lr.Attributes
	native := attr(a, "session.id")
	if native == "" {
		native = attr(res, "session.id")
	}
	if native == "" {
		return nil, false
	}
	sid := model.SessionID(claudecode.Name, native)
	ts := timestamp(lr, meta.ReceivedAt)
	version := attr(a, "app.version")
	if version == "" {
		version = attr(res, "service.version")
	}
	// fallback identifies a record without a natural id: the time plus
	// the process's sequence number.
	fallback := strconv.FormatInt(ts.UnixNano(), 10) + ":" + attr(a, "event.sequence")
	base := func(kind model.Kind, t time.Time, data map[string]any, key string) model.Event {
		return model.Event{
			ID: model.NewULID(t), V: model.SchemaVersion, TS: t.UTC(), ReceivedAt: meta.ReceivedAt.UTC(),
			Kind: kind, Agent: model.Agent{Name: claudecode.Name, Version: version}, Collector: model.CollectorOTLP,
			User: meta.User, SessionID: sid, ActorID: sid, TurnID: attr(a, "prompt.id"), Data: data, DedupKey: key,
		}
	}

	switch name {
	case "api_request":
		data := map[string]any{
			"model":              attr(a, "model"),
			"input_tokens":       integer(a, "input_tokens"),
			"output_tokens":      integer(a, "output_tokens"),
			"cache_read_tokens":  integer(a, "cache_read_tokens"),
			"cache_write_tokens": integer(a, "cache_creation_tokens"),
			"cost_source":        "reported",
		}
		cost, has := number(a, "cost_usd")
		if !has {
			if micros, ok := number(a, "cost_usd_micros"); ok {
				cost, has = micros/1e6, true
			}
		}
		if has {
			data["cost_usd"] = cost
		}
		if d, ok := number(a, "duration_ms"); ok {
			data["duration_ms"] = int64(d)
		}
		for attrKey, dataKey := range map[string]string{"request_id": "request_id", "query_source": "query_source", "speed": "speed", "agent.name": "agent_name"} {
			if v := attr(a, attrKey); v != "" {
				data[dataKey] = v
			}
		}
		key := attr(a, "request_id")
		if key == "" {
			key = attr(a, "client_request_id")
		}
		if key == "" {
			key = fallback
		}
		return []model.Event{base(model.KindUsage, ts, data, sid+":otlp:request:"+key)}, true

	case "user_prompt":
		data := map[string]any{}
		if n, ok := number(a, "prompt_length"); ok {
			data["prompt_chars"] = int(n)
		}
		// The prompt is "<REDACTED>" unless the user opted in to logging it.
		if p := attr(a, "prompt"); p != "" && !strings.HasPrefix(p, "<") {
			data["prompt"] = p
		}
		key := attr(a, "prompt.id")
		if key == "" {
			key = fallback
		}
		return []model.Event{base(model.KindTurnStart, ts, data, sid+":otlp:prompt:"+key)}, true

	case "tool_result":
		id, tool := attr(a, "tool_use_id"), attr(a, "tool_name")
		if id == "" {
			return nil, true
		}
		start := ts
		end := map[string]any{"tool_call_id": id, "tool": claudecode.NormalizeTool(tool), "ok": boolean(a, "success")}
		if d, ok := number(a, "duration_ms"); ok && d >= 0 && d < float64(24*time.Hour/time.Millisecond) {
			end["duration_ms"] = int64(d)
			start = ts.Add(-time.Duration(d) * time.Millisecond)
		}
		if e := attr(a, "error"); e != "" {
			end["error"] = e
		}
		// Same dedup keys as the hook events for this tool call.
		return []model.Event{
			base(model.KindToolStart, start, map[string]any{"tool_call_id": id, "tool": claudecode.NormalizeTool(tool), "tool_raw": tool}, sid+":"+id+":start"),
			base(model.KindToolEnd, ts, end, sid+":"+id+":end"),
		}, true
	}
	return nil, true
}

// timestamp picks the record's time: its timestamp, Claude Code's
// event.timestamp attribute, the observed time, or when it was received.
func timestamp(lr LogRecord, received time.Time) time.Time {
	if t := lr.TimeUnixNano; t != 0 && t <= 1<<63-1 {
		return time.Unix(0, int64(t))
	}
	if t, err := time.Parse(time.RFC3339Nano, attr(lr.Attributes, "event.timestamp")); err == nil {
		return t
	}
	if t := lr.ObservedTimeUnixNano; t != 0 && t <= 1<<63-1 {
		return time.Unix(0, int64(t))
	}
	return received
}

// attr returns an attribute as a string, whatever its type.
func attr(m map[string]any, k string) string {
	switch v := m[k].(type) {
	case nil:
		return ""
	case string:
		return v
	case bool:
		return strconv.FormatBool(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64)
	case []byte:
		return base64.StdEncoding.EncodeToString(v)
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

// number reads a numeric attribute, which agents send as int, double or
// string. NaN and infinities count as missing (they can't be stored).
func number(m map[string]any, k string) (float64, bool) {
	var f float64
	switch v := m[k].(type) {
	case int64:
		f = float64(v)
	case float64:
		f = v
	case string:
		var err error
		if f, err = strconv.ParseFloat(strings.TrimSpace(v), 64); err != nil {
			return 0, false
		}
	default:
		return 0, false
	}
	return f, !math.IsNaN(f) && !math.IsInf(f, 0)
}

func integer(m map[string]any, k string) int64 {
	f, _ := number(m, k)
	return int64(f)
}

func boolean(m map[string]any, k string) bool {
	if b, ok := m[k].(bool); ok {
		return b
	}
	b, _ := strconv.ParseBool(attr(m, k))
	return b
}
