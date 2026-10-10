package cline

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/elephaant/shiplino/pkg/adapters"
	"github.com/elephaant/shiplino/pkg/model"
	"github.com/elephaant/shiplino/pkg/pricing"
)

// Token usage and cost come from Cline's own task files, checked against
// the source of cline/cline (main and v4.0.12) on 2026-10-10. Cline
// rewrites each file whole on every save, so they are read as documents
// (adapters.DocumentParser), and only usage is taken from them: the
// prompts, replies and tool calls in them are never read into events.
//
// SDK hosts (CLI, Kanban, the desktop app and the SDK build of the VS
// Code extension) write sdk/packages/core/docs/messages-contract-v1.md:
//
//	~/.cline/data/sessions/<id>/<id>.messages.json   the main agent
//	~/.cline/data/sessions/<id>/<agent>.messages.json a subagent or team task
//	{version: 1, sessionId, agent, messages: [{id, role, ts, modelInfo {id,
//	 provider}, metrics {inputTokens, outputTokens, cacheReadTokens,
//	 cacheWriteTokens, cost}}]}
//
// metrics is stamped once, on the assistant message that ends a model
// call, with that call's usage, so each message id is counted once.
// inputTokens follows the AI SDK convention (cache reads and writes
// included); Cline's own telemetry subtracts them for the uncached part
// (legacyTokenUsageFromUsageEvent), and so does Shiplino. A task from the
// older extension that is resumed in an SDK host starts with its history
// copied in, the old totals on the last copied assistant message, and a
// "legacy conversation" notice after it; usage before that notice was
// already counted from the old task's own files, so it is skipped.
//
// The classic extension (VS Code and its forks, JetBrains, the older CLI)
// writes ui_messages.json per task, in the editor's global storage
// (<config>/<editor>/User/globalStorage/saoudrizwan.claude-dev/tasks/<id>/)
// or ~/.cline/data/tasks/<id>/. Each model call is a say "api_req_started"
// message whose text is JSON {tokensIn (uncached), tokensOut, cacheWrites,
// cacheReads, cost, cancelReason, streamingFailedMessage}, keyed by its ts.
// Cline updates it in place while the response streams, and once more
// when the call ends (some providers send usage only after the stream).
// A call is counted when it is final: a later call has started, it was
// cancelled or failed, or the file has been quiet for settleAfter.
//
// Cline prices every call itself (cost: from the provider when it reports
// one, else from Cline's model catalog), so cost_source is "reported".
// When Cline's cost is 0 (a model it has no price for, or a plan), the
// call is priced from Shiplino's table when the model is known.

// settleAfter is how long the classic extension's file must be quiet
// before its last call's usage is taken as final. Cline saves the file
// on every usage update and every finished message, so a call still
// streaming rarely leaves it untouched this long.
const settleAfter = 2 * time.Minute

// legacyResumeNotice starts the message Cline adds after the history of a
// classic task it resumes in an SDK host (apps/vscode/src/sdk/
// legacy-task-handling.ts).
const legacyResumeNotice = "Warning: this is a legacy conversation"

// ParseTranscriptDocument implements adapters.DocumentParser.
func (Adapter) ParseTranscriptDocument(doc []byte, meta adapters.TranscriptMeta) ([]model.Event, bool, error) {
	if meta.Warmup {
		return nil, false, nil
	}
	name := filepath.Base(meta.Path)
	switch {
	case name == "ui_messages.json":
		return parseUIMessages(doc, meta)
	case strings.HasSuffix(name, ".messages.json"):
		evs, err := parseSessionMessages(doc, meta)
		return evs, false, err
	}
	return nil, false, nil
}

// uiMessage is the part of a classic ClineMessage that matters here.
type uiMessage struct {
	TS        int64  `json:"ts"`
	Type      string `json:"type"`
	Say       string `json:"say"`
	Text      string `json:"text"`
	ModelInfo *struct {
		ModelID string `json:"modelId"`
	} `json:"modelInfo"`
}

// apiReqInfo is the text of an api_req_started message.
type apiReqInfo struct {
	TokensIn        *int64   `json:"tokensIn"`
	TokensOut       int64    `json:"tokensOut"`
	CacheWrites     int64    `json:"cacheWrites"`
	CacheReads      int64    `json:"cacheReads"`
	Cost            *float64 `json:"cost"`
	CancelReason    string   `json:"cancelReason"`
	StreamingFailed string   `json:"streamingFailedMessage"`
}

func parseUIMessages(doc []byte, meta adapters.TranscriptMeta) ([]model.Event, bool, error) {
	var msgs []uiMessage
	if err := json.Unmarshal(doc, &msgs); err != nil {
		return nil, false, fmt.Errorf("cline ui_messages.json: %w", err)
	}
	task := filepath.Base(filepath.Dir(meta.Path))
	sid := model.SessionID(Name, task)
	quiet := !meta.ModTime.IsZero() && meta.ReceivedAt.Sub(meta.ModTime) >= settleAfter
	var reqs []int
	for i, m := range msgs {
		if m.Type == "say" && m.Say == "api_req_started" {
			reqs = append(reqs, i)
		}
	}
	var out []model.Event
	recheck := false
	for n, i := range reqs {
		m := msgs[i]
		var info apiReqInfo
		if json.Unmarshal([]byte(m.Text), &info) != nil || info.TokensIn == nil {
			continue // no usage yet (or a call that never got a response)
		}
		final := n < len(reqs)-1 || info.CancelReason != "" || info.StreamingFailed != "" || quiet
		if !final {
			recheck = true
			continue
		}
		modelID := ""
		if m.ModelInfo != nil {
			modelID = m.ModelInfo.ModelID
		}
		u := pricing.Usage{Input: *info.TokensIn, Output: info.TokensOut, CacheRead: info.CacheReads, CacheWrite5m: info.CacheWrites}
		ts := time.UnixMilli(m.TS).UTC()
		out = append(out, usageEvent(sid, sid, "", ts, modelID, u, info.Cost, meta, Name+":usage:"+task+":"+strconv.FormatInt(m.TS, 10)))
	}
	return out, recheck, nil
}

// sessionMessages is the part of an SDK messages.json that matters here.
type sessionMessages struct {
	SessionID string `json:"sessionId"`
	Messages  []struct {
		ID        string          `json:"id"`
		Role      string          `json:"role"`
		TS        int64           `json:"ts"`
		Content   json.RawMessage `json:"content"`
		ModelInfo *struct {
			ID string `json:"id"`
		} `json:"modelInfo"`
		Metrics *struct {
			InputTokens      int64    `json:"inputTokens"`
			OutputTokens     int64    `json:"outputTokens"`
			CacheReadTokens  int64    `json:"cacheReadTokens"`
			CacheWriteTokens int64    `json:"cacheWriteTokens"`
			Cost             *float64 `json:"cost"`
		} `json:"metrics"`
	} `json:"messages"`
}

func parseSessionMessages(doc []byte, meta adapters.TranscriptMeta) ([]model.Event, error) {
	var f sessionMessages
	if err := json.Unmarshal(doc, &f); err != nil {
		return nil, fmt.Errorf("cline messages.json: %w", err)
	}
	root, agent := sessionActor(f.SessionID, meta.Path)
	if root == "" {
		return nil, nil
	}
	sid := model.SessionID(Name, root)
	actor := sid
	if agent != "" {
		actor = sid + "/sub:" + agent
	}
	// Usage before the legacy notice was counted from the classic files.
	from := 0
	for i, m := range f.Messages {
		if m.Role == "user" && bytes.Contains(m.Content, []byte(legacyResumeNotice)) {
			from = i + 1
		}
	}
	var out []model.Event
	for _, m := range f.Messages[from:] {
		x := m.Metrics
		if m.Role != "assistant" || x == nil {
			continue
		}
		modelID := ""
		if m.ModelInfo != nil {
			modelID = m.ModelInfo.ID
		}
		ts := meta.ModTime.UTC()
		if m.TS > 0 {
			ts = time.UnixMilli(m.TS).UTC()
		}
		u := pricing.Usage{Input: max(x.InputTokens-x.CacheReadTokens-x.CacheWriteTokens, 0), Output: x.OutputTokens, CacheRead: x.CacheReadTokens, CacheWrite5m: x.CacheWriteTokens}
		id := m.ID
		if id == "" {
			id = root + ":" + agent + ":" + strconv.FormatInt(m.TS, 10)
		}
		out = append(out, usageEvent(sid, actor, m.ID, ts, modelID, u, x.Cost, meta, Name+":usage:"+id))
	}
	return out, nil
}

// sessionActor is the root session and subagent of an SDK messages file:
// from its sessionId (<root>, <root>__<agent> or
// <root>__teamtask__<agent>__<task>), else from its path.
func sessionActor(sessionID, path string) (root, agent string) {
	if sessionID == "" {
		root = filepath.Base(filepath.Dir(path))
		stem := strings.TrimSuffix(filepath.Base(path), ".messages.json")
		if stem != root {
			agent, _, _ = strings.Cut(stem, "__")
		}
		return root, agent
	}
	if r, rest, ok := strings.Cut(sessionID, "__teamtask__"); ok && r != "" {
		if i := strings.LastIndex(rest, "__"); i > 0 {
			return r, rest[:i]
		}
		return r, rest
	}
	root, agent, _ = strings.Cut(sessionID, "__")
	return root, agent
}

// usageEvent is one model call's usage with Cline's own cost.
func usageEvent(sid, actor, messageID string, ts time.Time, modelID string, u pricing.Usage, cost *float64, meta adapters.TranscriptMeta, key string) model.Event {
	d := map[string]any{
		"input_tokens": u.Input, "output_tokens": u.Output, "cache_read_tokens": u.CacheRead, "cache_write_tokens": u.CacheWrite5m,
	}
	if modelID != "" {
		d["model"] = modelID
	}
	if messageID != "" {
		d["message_id"] = messageID
	}
	u.At = ts
	switch c, ok := pricing.Default().Cost(modelID, u); {
	case cost != nil && *cost > 0:
		d["cost_usd"], d["cost_source"] = *cost, "reported"
	case modelID != "" && ok:
		d["cost_usd"], d["cost_source"] = c, "computed"
	default:
		d["cost_source"] = "unpriced"
	}
	e := model.Event{
		ID: model.NewULID(ts), V: model.SchemaVersion, TS: ts, ReceivedAt: meta.ReceivedAt.UTC(),
		Kind: model.KindUsage, Agent: model.Agent{Name: Name}, Collector: model.CollectorTranscript,
		User: meta.User, SessionID: sid, ActorID: actor, Data: d, DedupKey: key,
	}
	if actor != sid {
		e.ParentActor, e.ActorType = sid, "subagent"
	}
	if meta.Ref != "" {
		e.Raw = &model.RawRef{Ref: meta.Ref}
	}
	return e
}

// TranscriptRoots implements adapters.TranscriptDiscoverer: SDK session
// files and classic task files, in Cline's data folder and in the global
// storage of VS Code and its forks.
func (Adapter) TranscriptRoots(userHome string, _, _ time.Time) []string {
	data := dataDir(userHome)
	sessions := filepath.Join(data, "sessions")
	if d := os.Getenv("CLINE_SESSION_DATA_DIR"); d != "" {
		sessions = d
	}
	roots := []string{
		filepath.Join(sessions, "*", "*.messages.json"),
		filepath.Join(data, "tasks", "*", "ui_messages.json"),
	}
	for _, base := range editorConfigDirs(userHome) {
		roots = append(roots, filepath.Join(base, "*", "User", "globalStorage", "saoudrizwan.claude-dev", "tasks", "*", "ui_messages.json"))
	}
	return roots
}

// dataDir is Cline's data folder: $CLINE_DATA_DIR, $CLINE_DIR/data or
// ~/.cline/data (sdk/packages/shared/src/storage/paths.ts).
func dataDir(home string) string {
	if d := os.Getenv("CLINE_DATA_DIR"); d != "" {
		return d
	}
	if d := os.Getenv("CLINE_DIR"); d != "" {
		return filepath.Join(d, "data")
	}
	return filepath.Join(home, ".cline", "data")
}

// editorConfigDirs are where VS Code and its forks (Cursor, Windsurf,
// VSCodium, …) keep their per-editor folders.
func editorConfigDirs(home string) []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{filepath.Join(home, "Library", "Application Support")}
	case "windows":
		return []string{filepath.Join(home, "AppData", "Roaming")}
	}
	dirs := []string{filepath.Join(home, ".config")}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" && x != dirs[0] {
		dirs = append(dirs, x)
	}
	return dirs
}
