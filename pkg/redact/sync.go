package redact

import (
	"crypto/sha256"
	"encoding/hex"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/elephaant/shiplino/pkg/model"
	"github.com/elephaant/shiplino/pkg/projects"
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
	"trigger": true, "denied": true,
	// a shell command's program name: a checked short token (see Program), never the command
	"program": true,
	// files (project-relative after localPaths) and line counts
	"path": true, "file_path": true, "files": true, "file_paths": true, "paths": true, "lines_added": true,
	"lines_removed": true, "lines_source": true, "files_changed": true, "patch_omitted": true,
	// git
	"sha": true, "branch": true, "to": true, "number": true, "url": true, "state": true, "action": true, "head": true,
	// plan progress (counts only)
	"plan_total": true, "plan_done": true,
}

// freeKeys are synced fields whose value may be any short text: paths,
// the generic waiting text and a pull request URL. Every other synced
// string must be a token (an id, name, enum or ref), so free text can't
// ride along in a field like "status" from a custom agent.
var freeKeys = map[string]bool{"path": true, "file_path": true, "files": true, "file_paths": true, "paths": true,
	"input_summary": true, "message": true, "url": true}

// token is what a non-free synced string must look like.
var token = regexp.MustCompile(`^[A-Za-z0-9_.:/@+\-]{1,128}$`)

// prURL is the only URL shape that syncs.
var prURL = regexp.MustCompile(`^https://[A-Za-z0-9.\-]+(:[0-9]+)?/[A-Za-z0-9_.\-/]+$`)

// maxSyncString caps any string that leaves the machine.
const maxSyncString = 512

// maxSyncTitle caps a session title sent with titles on.
const maxSyncTitle = 120

// ForSync strips an event to what may be synced: local paths made
// project-relative (localPaths), the minimal capture level, then only
// the fields in syncKeys, with every string redacted
// and capped. A waiting event keeps its generic "Waiting for …" text and
// a file tool its path; nothing else of the conversation passes. With
// titles, the session title (redacted, 120 characters) is kept too. The
// OS user name and the pointer to local raw data are always removed.
//
// The sync client and the sync service both apply it, so an older or
// modified client can't make the service store content.
func (r *Redactor) ForSync(e *model.Event, titles bool) {
	e.User, e.Raw = "", nil
	r.envelope(e)
	if e.Data == nil {
		localPaths(e)
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
	localPaths(e)
	r.Event(e, Minimal)
	for k, v := range e.Data {
		keep := syncKeys[k] && (k != "url" || e.Kind == model.KindGitPR) || // a fetched URL can carry anything
			k == "message" && e.Kind == model.KindWaitingStart || k == "input_summary" && pathTools[str(e.Data, "tool")]
		if !keep {
			delete(e.Data, k)
			continue
		}
		switch {
		case k == "model_usage":
			v = numbersOnly(v)
		case k == "url":
			if s, _ := v.(string); !prURL.MatchString(s) {
				v = nil
			}
		case freeKeys[k]:
			v = capStrings(r.value(v))
		default:
			if s, ok := v.(string); ok && !token.MatchString(s) {
				v = nil
			} else {
				v = capStrings(v)
			}
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

// envelope cleans the event's own fields: ids and refs are redacted and
// capped, the dedup key is hashed (keys can embed a title fingerprint or
// a path), and the git remote is reduced to host/owner/repo (no
// credentials).
func (r *Redactor) envelope(e *model.Event) {
	clean := func(s string) string { return capStrings(r.Text(s)).(string) }
	e.SessionID, e.ActorID, e.ParentActor, e.TurnID = clean(e.SessionID), clean(e.ActorID), clean(e.ParentActor), clean(e.TurnID)
	e.MachineID, e.ActorType = clean(e.MachineID), clean(e.ActorType)
	for _, s := range []*string{&e.Agent.Version, &e.Agent.Surface} {
		if !token.MatchString(*s) {
			*s = ""
		}
	}
	if e.DedupKey != "" {
		sum := sha256.Sum256([]byte(e.DedupKey))
		e.DedupKey = "h:" + hex.EncodeToString(sum[:16])
	}
	if p := e.Project; p != nil {
		cp := *p
		cp.ID = clean(cp.ID)
		if n := projects.NormalizeRemote(cp.Remote); n != "" {
			cp.Remote = n
		} else if !token.MatchString(cp.Remote) || strings.ContainsAny(cp.Remote, "@:") {
			cp.Remote = "" // already host/owner/repo, or nothing safe to send
		}
		for _, s := range []*string{&cp.Branch, &cp.Head} {
			if !token.MatchString(*s) {
				*s = ""
			}
		}
		e.Project = &cp
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
		out := make([]any, 0, len(s)) // a new list: the caller's event stays as it was
		for _, x := range s {
			if x = capStrings(x); x != nil {
				out = append(out, x)
			}
		}
		return out
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

// pathKeys are data fields holding a file or folder path.
var pathKeys = []string{"path", "file_path", "cwd", "transcript_path"}

// pathListKeys are data fields holding a list of paths.
var pathListKeys = []string{"files", "file_paths", "paths"}

// localPaths keeps local paths from leaving the machine: paths
// become relative to the project root, paths outside it become "…/" plus
// their base name, and the project's absolute folders are dropped (its
// id, remote, branch and head stay).
func localPaths(e *model.Event) {
	root := ""
	if p := e.Project; p != nil {
		root = p.RepoRoot
		if root == "" {
			for _, prefix := range []string{"local:", "dir:"} {
				if rest, ok := strings.CutPrefix(p.ID, prefix); ok {
					root = rest
				}
			}
		}
		if root == "" {
			root = p.CWD
		}
		cp := *p
		cp.CWD, cp.RepoRoot = "", ""
		e.Project = &cp
	}
	d := e.Data
	if d == nil {
		return
	}
	for _, k := range pathKeys {
		if s, ok := d[k].(string); ok {
			d[k] = relPath(root, s)
		}
	}
	if s, ok := d["input_summary"].(string); ok && pathTools[str(d, "tool")] {
		parts := strings.Split(s, ", ") // some agents list several files
		for i, p := range parts {
			parts[i] = relPath(root, p)
		}
		d["input_summary"] = strings.Join(parts, ", ")
	}
	for _, k := range pathListKeys {
		list, ok := d[k].([]any)
		if !ok {
			continue
		}
		out := make([]any, len(list))
		for i, v := range list {
			if s, ok := v.(string); ok {
				v = relPath(root, s)
			}
			out[i] = v
		}
		d[k] = out
	}
}

// relPath makes an absolute path relative to root ("." for root itself);
// one outside root becomes "…/<base name>". Relative paths are kept.
func relPath(root, p string) string {
	if !isAbs(p) {
		return p
	}
	sp := filepath.ToSlash(p)
	if r := strings.TrimRight(filepath.ToSlash(root), "/"); r != "" && isAbs(root) {
		if sp == r {
			return "."
		}
		if rest, ok := strings.CutPrefix(sp, r+"/"); ok {
			return rest
		}
	}
	return "…/" + path.Base(strings.ReplaceAll(sp, `\`, "/"))
}

// isAbs accepts Unix and Windows absolute paths on every OS, since events
// may describe either.
func isAbs(p string) bool {
	return filepath.IsAbs(p) || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) ||
		len(p) > 2 && p[1] == ':' && (p[2] == '\\' || p[2] == '/')
}
