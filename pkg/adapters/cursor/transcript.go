// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package cursor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/elephaant/shiplino/pkg/adapters"
	"github.com/elephaant/shiplino/pkg/model"
)

// Agent transcripts, checked against local Cursor transcripts on
// 2026-10-10, for sessions the hooks didn't see (history from before
// setup, or hooks disabled):
//
//	~/.cursor/projects/<workspace-slug>/agent-transcripts/<conversation>/<conversation>.jsonl
//	                                  .../<conversation>/subagents/<subagent>.jsonl
//
// One JSON object per line:
//
//	{"role":"user","message":{"content":[{"type":"text","text":…}]}}
//	    the prompt in <user_query>, the local time in <timestamp>   → turn.start
//	{"role":"assistant","message":{"content":[{"type":"tool_use","name":…,"input":…}, …]}}
//	                                                                → tool.start + tool.end,
//	                                                                  shell.exec, file.edit, mcp.call
//	{"type":"turn_ended","status":"success|error|aborted","error":…} → turn.end
//
// The format has no per-line timestamps, no tool-call ids, no tool results,
// no model and no token counts. So tool calls count as succeeded, shell
// commands have no exit code, diff counts are computed from the edit, and
// times are the minute of the last <timestamp> plus a millisecond per line
// to keep order (the file's modification time before the first one).
// Ids come from the file path, exactly as the hook adapter derives them
// from transcript_path, and dedup keys from the line's byte offset (the
// files are append-only). The workspace folder isn't recorded either: it
// is read from the project's .workspace-trusted file, or found by matching
// the slug against existing folders.

type transcriptLine struct {
	Role    string `json:"role"`
	Type    string `json:"type"`
	Status  string `json:"status"`
	Error   string `json:"error"`
	Message struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	} `json:"message"`
}

var (
	queryRE = regexp.MustCompile(`(?s)<user_query>\s*(.*?)\s*</user_query>`)
	stampRE = regexp.MustCompile(`<timestamp>([^<]*)</timestamp>`)
)

// ParseTranscriptLine implements adapters.TranscriptParser. It keeps the
// clock and the workspace folder in meta.State.
func (Adapter) ParseTranscriptLine(raw []byte, meta adapters.TranscriptMeta) ([]model.Event, error) {
	var l transcriptLine
	if err := json.Unmarshal(raw, &l); err != nil {
		return nil, fmt.Errorf("cursor transcript: %w", err)
	}
	t, ok := newTranscript(meta)
	if !ok {
		return nil, nil
	}
	var prompt string
	query := false // an empty query (e.g. images only) is still a prompt
	if l.Role == "user" {
		for _, c := range l.Message.Content {
			if m := stampRE.FindStringSubmatch(c.Text); m != nil {
				if ts, ok := parseStamp(m[1]); ok {
					t.setClock(ts)
				}
			}
			if m := queryRE.FindStringSubmatch(c.Text); m != nil && !query {
				prompt, query = m[1], true
			}
		}
	}
	t.tick()
	if meta.Warmup {
		return nil, nil
	}

	switch {
	case l.Role == "user" && query:
		return []model.Event{t.event(model.KindTurnStart, "turn", map[string]any{
			"prompt": prompt, "prompt_chars": len([]rune(prompt)), "transcript_path": meta.Path,
		})}, nil
	case l.Role == "assistant":
		var out []model.Event
		for i, c := range l.Message.Content {
			if c.Type == "tool_use" && c.Name != "" {
				out = append(out, t.tool(i, c.Name, c.Input)...)
			}
		}
		return out, nil
	case l.Type == "turn_ended":
		status := "ok"
		switch l.Status {
		case "aborted":
			status = "interrupted"
		case "error":
			status = "error"
		}
		return []model.Event{t.event(model.KindTurnEnd, "turnend", compact(map[string]any{"status": status, "error": l.Error}))}, nil
	}
	return nil, nil
}

// transcript builds events for one line of one transcript file.
type transcript struct {
	meta     adapters.TranscriptMeta
	st       map[string]string
	sid      string
	actor    string
	native   string // conversation or subagent id
	subagent bool
	off      string // byte offset of the line
}

func newTranscript(meta adapters.TranscriptMeta) (*transcript, bool) {
	conv, ok := conversationPath(meta.Path)
	if !ok {
		return nil, false
	}
	st := meta.State
	if st == nil {
		st = map[string]string{}
	}
	t := &transcript{meta: meta, st: st, sid: model.SessionID(Name, conv), native: conv}
	t.actor = t.sid
	if parent, sub, ok := subagentPath(meta.Path); ok {
		t.sid = model.SessionID(Name, parent)
		t.actor, t.native, t.subagent = t.sid+"/sub:"+sub, sub, true
	}
	if i := strings.LastIndexByte(meta.Ref, '#'); i >= 0 {
		t.off = meta.Ref[i+1:]
	}
	if _, done := st["cwd"]; !done {
		st["cwd"] = workspaceDir(meta.Path)
	}
	return t, true
}

// setClock moves the clock to a <timestamp>, never backwards: the tag has
// minute precision while the clock advances a millisecond per line.
func (t *transcript) setClock(ts time.Time) {
	if cur, ok := t.clock(); ok && t.st["clock_source"] == "agent" && !ts.After(cur) {
		return
	}
	t.st["clock"] = strconv.FormatInt(ts.UnixMilli(), 10)
	t.st["clock_source"] = "agent"
}

func (t *transcript) clock() (time.Time, bool) {
	ms, err := strconv.ParseInt(t.st["clock"], 10, 64)
	return time.UnixMilli(ms).UTC(), err == nil
}

// tick advances the clock by a millisecond, starting from the file's
// modification time when no <timestamp> has been seen yet.
func (t *transcript) tick() {
	cur, ok := t.clock()
	switch {
	case !ok:
		cur = t.meta.ModTime
		if cur.IsZero() {
			cur = t.meta.ReceivedAt
		}
	default:
		cur = cur.Add(time.Millisecond)
	}
	t.st["clock"] = strconv.FormatInt(cur.UnixMilli(), 10)
}

func (t *transcript) event(kind model.Kind, key string, data map[string]any) model.Event {
	ts, _ := t.clock()
	e := model.Event{
		ID: model.NewULID(ts), V: model.SchemaVersion, TS: ts, ReceivedAt: t.meta.ReceivedAt.UTC(),
		Kind: kind, Agent: model.Agent{Name: Name}, Collector: model.CollectorTranscript,
		User: t.meta.User, SessionID: t.sid, ActorID: t.actor, Data: data,
		DedupKey: t.actor + ":t" + t.off + ":" + key,
	}
	if t.subagent {
		e.ParentActor, e.ActorType = t.sid, "subagent"
	}
	if cwd := t.st["cwd"]; cwd != "" {
		e.Project = &model.Project{CWD: cwd}
	}
	if t.meta.Ref != "" {
		e.Raw = &model.RawRef{Ref: t.meta.Ref}
	}
	return e
}

// tool turns the i-th tool_use block of a line into a start/end pair
// plus the events derived from its input. The transcript has no tool-call
// id, so one is made from the line's position.
func (t *transcript) tool(i int, name string, input json.RawMessage) []model.Event {
	id := fmt.Sprintf("%s@%s.%d", t.native, t.off, i)
	key := fmt.Sprintf("tool%d", i)
	norm := NormalizeTool(name)
	in := toolInput(input)
	summary := summarize(name, input)
	edits := fileEdits(name, in, input, t.st["cwd"])
	if name == "ApplyPatch" && len(edits) > 0 {
		summary, _ = edits[0]["path"].(string)
	}
	out := []model.Event{
		t.event(model.KindToolStart, key+":start", map[string]any{"tool_call_id": id, "tool": norm, "tool_raw": name, "input_summary": summary}),
		t.event(model.KindToolEnd, key+":end", map[string]any{"tool_call_id": id, "tool": norm, "ok": true}),
	}
	switch {
	case name == "Shell":
		out = append(out, t.event(model.KindShellExec, key+":shell", compact(map[string]any{
			"command": str(in["command"]), "tool_call_id": id, "cwd": first(str(in["working_directory"]), t.st["cwd"]),
		})))
	case name == "CallMcpTool" || (name == "CallDynamicTool" && in["mcpDetails"] != nil):
		out = append(out, t.event(model.KindMCPCall, key+":mcp", compact(map[string]any{
			"server": first(str(in["server"]), str(in["namespace"])), "tool": str(in["toolName"]), "ok": true,
		})))
	}
	for j, d := range edits {
		out = append(out, t.event(model.KindFileEdit, fmt.Sprintf("%s:file%d", key, j), d))
	}
	return out
}

// toolInput decodes a tool's input object (sometimes JSON in a string).
func toolInput(raw json.RawMessage) map[string]json.RawMessage {
	var in map[string]json.RawMessage
	if json.Unmarshal(raw, &in) != nil {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			_ = json.Unmarshal([]byte(s), &in)
		}
	}
	return in
}

// fileEdits returns file.edit data for the tools that change files, with
// line counts computed from the edit itself.
func fileEdits(name string, in map[string]json.RawMessage, raw json.RawMessage, cwd string) []map[string]any {
	edit := func(path, op string, added, removed int, counted bool) map[string]any {
		d := map[string]any{"path": joinPath(cwd, path), "op": op}
		if counted {
			d["lines_added"], d["lines_removed"], d["lines_source"] = added, removed, "computed"
		}
		return d
	}
	path := str(in["path"])
	switch name {
	case "Write":
		if path != "" {
			return []map[string]any{edit(path, "create", len(splitLines(str(in["contents"]))), 0, true)}
		}
	case "StrReplace":
		if path != "" {
			a, r := lineDiff(str(in["old_string"]), str(in["new_string"]))
			return []map[string]any{edit(path, "modify", a, r, true)}
		}
	case "Delete":
		if path != "" {
			return []map[string]any{edit(path, "delete", 0, 0, false)}
		}
	case "ApplyPatch":
		patch := str(in["raw"])
		if patch == "" {
			_ = json.Unmarshal(raw, &patch)
		}
		var out []map[string]any
		for _, f := range parsePatch(patch) {
			out = append(out, edit(f.path, f.op, f.added, f.removed, f.op != "delete"))
		}
		return out
	}
	return nil
}

type patchFile struct {
	path, op       string
	added, removed int
}

// parsePatch reads an ApplyPatch body ("*** Begin Patch", then "*** Add
// File: p", "*** Update File: p" or "*** Delete File: p" sections).
func parsePatch(patch string) []patchFile {
	var out []patchFile
	for _, l := range strings.Split(patch, "\n") {
		l = strings.TrimRight(l, "\r")
		op := ""
		switch {
		case strings.HasPrefix(l, "*** Add File: "):
			op = "create"
		case strings.HasPrefix(l, "*** Update File: "):
			op = "modify"
		case strings.HasPrefix(l, "*** Delete File: "):
			op = "delete"
		case strings.HasPrefix(l, "*** Move to: ") && len(out) > 0:
			out[len(out)-1].path = strings.TrimSpace(l[len("*** Move to: "):])
			continue
		case strings.HasPrefix(l, "***") || len(out) == 0:
			continue
		case strings.HasPrefix(l, "+"):
			out[len(out)-1].added++
			continue
		case strings.HasPrefix(l, "-"):
			out[len(out)-1].removed++
			continue
		default:
			continue
		}
		_, p, _ := strings.Cut(l, ": ")
		out = append(out, patchFile{path: strings.TrimSpace(p), op: op})
	}
	return out
}

// parseStamp reads Cursor's <timestamp> text, e.g. "Thursday, Aug 6, 2026,
// 10:41 AM (UTC+5:30)", in the offset it names.
func parseStamp(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	main, zone, ok := strings.Cut(s, " (")
	if !ok || !strings.HasSuffix(zone, ")") {
		return time.Time{}, false
	}
	off, ok := utcOffset(strings.TrimSuffix(zone, ")"))
	if !ok {
		return time.Time{}, false
	}
	if day, rest, ok := strings.Cut(main, ", "); ok && strings.HasSuffix(day, "day") {
		main = rest
	}
	loc := time.FixedZone("", off)
	for _, layout := range []string{"Jan 2, 2006, 3:04 PM", "January 2, 2006, 3:04 PM", "Jan 2, 2006, 15:04"} {
		if t, err := time.ParseInLocation(layout, main, loc); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// utcOffset parses "UTC", "UTC+5:30" or "UTC-7" into seconds east of UTC.
func utcOffset(z string) (int, bool) {
	z, ok := strings.CutPrefix(z, "UTC")
	if !ok {
		z, ok = strings.CutPrefix(z, "GMT")
	}
	if !ok {
		return 0, false
	}
	if z == "" {
		return 0, true
	}
	sign := 1
	switch z[0] {
	case '+':
	case '-':
		sign = -1
	default:
		return 0, false
	}
	h, m, _ := strings.Cut(z[1:], ":")
	hh, err := strconv.Atoi(h)
	if err != nil || hh > 14 {
		return 0, false
	}
	mm := 0
	if m != "" {
		if mm, err = strconv.Atoi(m); err != nil || mm >= 60 {
			return 0, false
		}
	}
	return sign * (hh*3600 + mm*60), true
}

// pathParts splits a transcript path in either separator style.
func pathParts(p string) []string {
	return strings.Split(strings.ReplaceAll(p, `\`, "/"), "/")
}

// conversationPath returns the conversation id of a transcript at
// agent-transcripts/<id>/<id>.jsonl, agent-transcripts/<id>.jsonl or a
// subagent's file under its parent.
func conversationPath(p string) (string, bool) {
	parts := pathParts(p)
	n := len(parts)
	if n < 2 || !strings.HasSuffix(parts[n-1], ".jsonl") {
		return "", false
	}
	stem := strings.TrimSuffix(parts[n-1], ".jsonl")
	switch {
	case n >= 4 && parts[n-2] == "subagents" && parts[n-4] == "agent-transcripts":
		return stem, true
	case n >= 3 && parts[n-3] == "agent-transcripts" && parts[n-2] == stem:
		return stem, true
	case parts[n-2] == "agent-transcripts":
		return stem, true
	}
	return "", false
}

// subagentPath reports the parent conversation and subagent id of a
// subagent transcript: agent-transcripts/<parent>/subagents/<id>.jsonl.
func subagentPath(p string) (parent, sub string, ok bool) {
	parts := pathParts(p)
	if n := len(parts); n >= 4 && parts[n-2] == "subagents" && parts[n-4] == "agent-transcripts" {
		return parts[n-3], strings.TrimSuffix(parts[n-1], ".jsonl"), true
	}
	return "", "", false
}

var workspaces sync.Map // project folder → workspace folder ("" if unknown)

// workspaceDir finds the workspace folder of a transcript's project:
// ~/.cursor/projects/<slug> records it in .workspace-trusted when the
// folder was trusted; otherwise the slug (the path with every run of
// other characters turned into "-") is matched against existing folders.
func workspaceDir(transcript string) string {
	dir := filepath.Dir(transcript)
	for filepath.Base(dir) != "agent-transcripts" {
		next := filepath.Dir(dir)
		if next == dir {
			return ""
		}
		dir = next
	}
	project := filepath.Dir(dir)
	if v, ok := workspaces.Load(project); ok {
		return v.(string)
	}
	var trusted struct {
		WorkspacePath string `json:"workspacePath"`
	}
	ws := ""
	if b, err := os.ReadFile(filepath.Join(project, ".workspace-trusted")); err == nil && json.Unmarshal(b, &trusted) == nil && (filepath.IsAbs(trusted.WorkspacePath) || strings.HasPrefix(trusted.WorkspacePath, "/")) {
		ws = trusted.WorkspacePath
	} else {
		ws = resolveSlug(filepath.Base(project))
	}
	workspaces.Store(project, ws)
	return ws
}

var slugRE = regexp.MustCompile(`[^A-Za-z0-9]+`)

// resolveSlug finds the existing folder whose path gives slug. On Windows
// the slug is assumed to start with the drive letter (not verified).
func resolveSlug(slug string) string {
	parts := strings.Split(slug, "-")
	root := "/"
	if runtime.GOOS == "windows" {
		if len(parts) < 2 || len(parts[0]) != 1 {
			return ""
		}
		root, parts = strings.ToUpper(parts[0])+`:\`, parts[1:]
	}
	budget := 512 // directory reads, so odd slugs can't take long
	return walkSlug(root, parts, &budget)
}

// walkSlug walks down from dir, trying each way to split the remaining
// slug parts into folder names.
func walkSlug(dir string, rest []string, budget *int) string {
	if len(rest) == 0 {
		return dir
	}
	if *budget--; *budget < 0 || rest[0] == "" {
		return ""
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() && e.Type()&os.ModeSymlink == 0 {
			continue
		}
		name := strings.Trim(slugRE.ReplaceAllString(e.Name(), "-"), "-")
		k := strings.Count(name, "-") + 1
		if name == "" || k > len(rest) || strings.Join(rest[:k], "-") != name {
			continue
		}
		if r := walkSlug(filepath.Join(dir, e.Name()), rest[k:], budget); r != "" {
			return r
		}
	}
	return ""
}

// TranscriptRoots implements adapters.TranscriptDiscoverer: conversations
// and their subagents, under every workspace's project folder.
func (Adapter) TranscriptRoots(userHome string, _, _ time.Time) []string {
	base := filepath.Join(userHome, ".cursor", "projects", "*", "agent-transcripts", "*")
	return []string{filepath.Join(base, "*.jsonl"), filepath.Join(base, "subagents", "*.jsonl")}
}

func str(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}
