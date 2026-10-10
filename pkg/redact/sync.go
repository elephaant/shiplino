package redact

import (
	"github.com/elephaant/shiplino/pkg/model"
)

// syncKeys are the data fields that may leave the machine: names, ids,
// counts, timings, outcomes, paths and git references. Content (prompts,
// replies, commands, tool input and output, errors, diffs, commit and
// notification messages) is never on this list, and a field that isn't
// listed is dropped, so a field added later stays local until it's
// reviewed and listed here.
var syncKeys = map[string]bool{
	// tools, agents and models
	"tool": true, "tool_raw": true, "tool_call_id": true, "agent_type": true, "agent_id": true,
	"attribution": true, "child_session_id": true, "model": true, "speed": true, "inference_geo": true,
	"message_id": true, "request_id": true, "agent_version": true, "wrapped": true, "card_id": true,
	// tokens and cost
	"input_tokens": true, "output_tokens": true, "cache_read_tokens": true, "cache_write_tokens": true,
	"cache_write_1h_tokens": true, "reasoning_tokens": true, "web_searches": true, "tokens": true,
	"tokens_rounded": true, "tokens_source": true, "prompt_chars": true, "cost_usd": true, "cost_source": true,
	"total_cost_usd": true, "message_cost_usd": true, "report": true, "process": true, "correction": true,
	"model_usage": true,
	// outcomes and timing
	"ok": true, "exit_code": true, "duration_ms": true, "status": true, "reason": true, "signal": true,
	"interrupted": true, "stop_reason": true, "recoverable": true, "permission_mode": true, "source": true,
	"trigger": true,
	// files (project-relative after localPaths) and line counts
	"path": true, "file_path": true, "files": true, "file_paths": true, "paths": true, "lines_added": true,
	"lines_removed": true, "lines_source": true, "files_changed": true, "patch_omitted": true,
	// git
	"sha": true, "branch": true, "to": true, "number": true, "url": true, "state": true, "action": true, "head": true,
	// plan progress (counts only)
	"plan_total": true, "plan_done": true,
}

// maxSyncString caps any string that leaves the machine.
const maxSyncString = 512

// maxSyncTitle caps a session title sent with titles on.
const maxSyncTitle = 120

// ForSync strips an event to what may be synced: the minimal capture
// level, then only the fields in syncKeys, with every string redacted
// and capped. A waiting event keeps its generic "Waiting for …" text and
// a file tool its path; nothing else of the conversation passes. With
// titles, the session title (redacted, 120 characters) is kept too. The
// OS user name and the pointer to local raw data are always removed.
//
// The sync client and the sync service both apply it, so an older or
// modified client can't make the service store content.
func (r *Redactor) ForSync(e *model.Event, titles bool) {
	e.User, e.Raw = "", nil
	if e.Data == nil {
		return
	}
	var title, titleSource string
	if titles {
		if t, ok := e.Data["title"].(string); ok && (e.Kind == model.KindSessionStart || e.Kind == model.KindSessionUpdate) {
			title = r.Text(t)
			if rs := []rune(title); len(rs) > maxSyncTitle {
				title = string(rs[:maxSyncTitle]) + "…"
			}
			titleSource, _ = e.Data["title_source"].(string)
		}
	}
	r.Event(e, Minimal)
	for k, v := range e.Data {
		keep := syncKeys[k] && (k != "url" || e.Kind == model.KindGitPR) || // a fetched URL can carry anything
			k == "message" && e.Kind == model.KindWaitingStart || k == "input_summary" && pathTools[str(e.Data, "tool")]
		if !keep {
			delete(e.Data, k)
			continue
		}
		if k == "model_usage" {
			v = numbersOnly(v)
		} else {
			v = capStrings(v)
		}
		if v == nil {
			delete(e.Data, k)
		} else {
			e.Data[k] = v
		}
	}
	if title != "" {
		e.Data["title"] = title
		if titleSource != "" {
			e.Data["title_source"] = capStrings(titleSource)
		}
	}
}

// capStrings caps strings, including inside lists (files, paths).
func capStrings(v any) any {
	switch s := v.(type) {
	case string:
		if rs := []rune(s); len(rs) > maxSyncString {
			return string(rs[:maxSyncString])
		}
	case []any:
		for i, x := range s {
			s[i] = capStrings(x)
		}
	case map[string]any:
		return nil // structured values are only allowed where listed
	}
	return v
}

// numbersOnly keeps the numeric leaves of nested maps (per-model usage
// counts) and the map keys (model names).
func numbersOnly(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := map[string]any{}
	for k, x := range m {
		switch n := x.(type) {
		case float64, int, int64:
			out[capStrings(k).(string)] = n
		case map[string]any:
			out[capStrings(k).(string)] = numbersOnly(n)
		}
	}
	return out
}
