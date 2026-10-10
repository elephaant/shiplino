package opencode

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/elephaant/shiplino/pkg/adapters"
	"github.com/elephaant/shiplino/pkg/model"
	"github.com/elephaant/shiplino/pkg/pricing"
)

// Name is the adapter name used in `shiplino hook --agent opencode`.
const Name = "opencode"

func init() { adapters.Register(Adapter{}) }

// Adapter is the OpenCode adapter.
type Adapter struct{}

// Name implements adapters.Adapter.
func (Adapter) Name() string { return Name }

// payload is what the plugin (plugins/opencode/shiplino.js) writes.
type payload struct {
	SessionID string   `json:"session_id"`
	Event     string   `json:"hook_event_name"`
	Timestamp int64    `json:"timestamp"`
	CWD       string   `json:"cwd"`
	Lineage   []string `json:"lineage"`
	Version   string   `json:"version"`

	// sessions
	Title    string `json:"title"`
	ParentID string `json:"parent_id"`
	Agent    string `json:"agent"`

	// messages
	MessageID   string          `json:"message_id"`
	ModelID     string          `json:"model_id"`
	ProviderID  string          `json:"provider_id"`
	Tokens      *tokens         `json:"tokens"`
	Cost        *float64        `json:"cost"`
	Finish      string          `json:"finish"`
	Error       json.RawMessage `json:"error"` // {name, message}, or a string on tool parts
	CompletedAt int64           `json:"completed_at"`
	PartType    string          `json:"part_type"`
	Prompt      string          `json:"prompt"`

	// tool parts
	CallID         string          `json:"call_id"`
	Tool           string          `json:"tool"`
	Status         string          `json:"status"`
	Input          json.RawMessage `json:"tool_input"`
	StartedAt      int64           `json:"started_at"`
	EndedAt        int64           `json:"ended_at"`
	ExitCode       *int            `json:"exit_code"`
	ChildSessionID string          `json:"child_session_id"`
	Exists         *bool           `json:"exists"`
	FileDiff       *fileChange     `json:"file_diff"`
	Diff           string          `json:"diff"`
	Files          []fileChange    `json:"files"`
	FilePatches    []string        `json:"file_patches"`

	// permissions and questions
	RequestID  string   `json:"request_id"`
	Permission string   `json:"permission"`
	Patterns   []string `json:"patterns"`
	Message    string   `json:"message"`
	Reply      string   `json:"reply"`
}

type tokens struct {
	Input     int64 `json:"input"`
	Output    int64 `json:"output"`
	Reasoning int64 `json:"reasoning"`
	Cache     struct {
		Read  int64 `json:"read"`
		Write int64 `json:"write"`
	} `json:"cache"`
}

type fileChange struct {
	File      string `json:"file"` // file_diff (edit tool)
	Path      string `json:"path"` // files (apply_patch)
	MovePath  string `json:"move_path"`
	Type      string `json:"type"` // add | update | delete | move
	Additions *int   `json:"additions"`
	Deletions *int   `json:"deletions"`
}

type toolInput struct {
	FilePath  string `json:"filePath"`
	Path      string `json:"path"`
	Command   string `json:"command"`
	Workdir   string `json:"workdir"`
	OldString string `json:"oldString"`
	NewString string `json:"newString"`
	Content   string `json:"content"`
	Pattern   string `json:"pattern"`
	URL       string `json:"url"`
	Query     string `json:"query"`
	Desc      string `json:"description"`
	Agent     string `json:"subagent_type"`
}

// ParseHook implements adapters.Adapter.
func (Adapter) ParseHook(raw []byte, meta adapters.HookMeta) ([]model.Event, error) {
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("opencode: decode hook payload: %w", err)
	}
	if p.SessionID == "" {
		return nil, fmt.Errorf("opencode: hook payload has no session_id")
	}
	b := newBuilder(&p, meta)
	child := len(p.Lineage) > 0

	event := first(meta.Event, p.Event)
	switch event {
	case "session.created":
		if child {
			return []model.Event{b.subagent(model.KindSubagentStart, "running"), b.titled()}, nil
		}
		e := b.base(model.KindSessionStart, compact(map[string]any{"source": "new", "title": p.Title, "agent": p.Agent}))
		e.DedupKey = b.sid + ":session.start"
		return []model.Event{e}, nil
	case "session.updated":
		if p.Title == "" {
			return nil, nil
		}
		return []model.Event{b.titled()}, nil
	case "session.deleted":
		if child {
			return []model.Event{b.subagent(model.KindSubagentEnd, "done")}, nil
		}
		return b.one(model.KindSessionEnd, map[string]any{"reason": "deleted", "status": "ended"}), nil
	case "session.idle":
		if child {
			return []model.Event{b.subagent(model.KindSubagentEnd, "done")}, nil
		}
		return b.one(model.KindTurnEnd, map[string]any{"status": "ok"}), nil
	case "session.compacted":
		return b.one(model.KindCompact, map[string]any{"phase": "post"}), nil
	case "session.error":
		e := errorOf(p.Error)
		if e.Name == "MessageAbortedError" || (e.Name == "" && e.Message == "") {
			return nil, nil // the user stopped the turn; session.idle follows
		}
		return b.one(model.KindError, compact(map[string]any{"name": e.Name, "message": e.Message})), nil
	case "message.updated":
		if p.MessageID == "" || p.Tokens == nil {
			return nil, nil
		}
		return []model.Event{b.usage()}, nil
	case "message.part.updated":
		switch p.PartType {
		case "text":
			if p.MessageID == "" {
				return nil, nil
			}
			e := b.base(model.KindTurnStart, compact(map[string]any{"prompt": p.Prompt, "prompt_chars": len([]rune(p.Prompt)), "agent": p.Agent}))
			e.DedupKey = b.actor + ":turn:" + p.MessageID
			return []model.Event{e}, nil
		case "tool":
			return b.tool(), nil
		}
		return nil, nil
	case "permission.asked", "permission.updated":
		msg := strings.TrimSpace(p.Permission + " " + strings.Join(p.Patterns, ", "))
		e := b.base(model.KindWaitingStart, compact(map[string]any{
			"reason": "permission", "message": first(p.Message, msg), "permission": p.Permission, "tool_call_id": p.CallID,
		}))
		e.DedupKey = b.actor + ":waiting:" + first(p.RequestID, meta.EnvelopeID)
		return []model.Event{e}, nil
	case "question.asked":
		e := b.base(model.KindWaitingStart, compact(map[string]any{"reason": "question", "message": p.Message, "tool_call_id": p.CallID}))
		e.DedupKey = b.actor + ":waiting:" + first(p.RequestID, meta.EnvelopeID)
		return []model.Event{e}, nil
	case "permission.replied", "question.replied", "question.rejected":
		resolution := p.Reply
		switch event {
		case "question.replied":
			resolution = "answered"
		case "question.rejected":
			resolution = "rejected"
		}
		e := b.base(model.KindWaitingEnd, compact(map[string]any{"resolution": resolution}))
		e.DedupKey = b.actor + ":waiting:" + first(p.RequestID, meta.EnvelopeID) + ":end"
		return []model.Event{e}, nil
	}
	return nil, fmt.Errorf("%w: opencode %q", adapters.ErrUnknownEvent, event)
}

type builder struct {
	p      *payload
	meta   adapters.HookMeta
	sid    string // root session
	actor  string // this session: the root, or root/sub:…/sub:<id>
	parent string // parent actor of a child session
	ts     time.Time
}

func newBuilder(p *payload, meta adapters.HookMeta) builder {
	b := builder{p: p, meta: meta, ts: msTime(p.Timestamp, meta.ReceivedAt)}
	if len(p.Lineage) == 0 {
		b.sid = model.SessionID(Name, p.SessionID)
		b.actor = b.sid
		return b
	}
	b.sid = model.SessionID(Name, p.Lineage[0])
	b.parent = b.sid
	for _, id := range p.Lineage[1:] {
		b.parent += "/sub:" + id
	}
	b.actor = b.parent + "/sub:" + p.SessionID
	return b
}

func (b builder) base(kind model.Kind, data map[string]any) model.Event {
	e := model.Event{
		ID: model.NewULID(b.ts), V: model.SchemaVersion, TS: b.ts, ReceivedAt: b.meta.ReceivedAt.UTC(),
		Kind: kind, Agent: model.Agent{Name: Name, Version: b.p.Version, Surface: "cli"}, Collector: model.CollectorHook,
		MachineID: b.meta.MachineID, User: b.meta.User, SessionID: b.sid, ActorID: b.actor, Data: data,
		DedupKey: b.actor + ":" + string(kind) + ":" + b.meta.EnvelopeID,
	}
	if b.parent != "" {
		e.ParentActor, e.ActorType = b.parent, b.agentType()
	}
	if b.p.CWD != "" {
		e.Project = &model.Project{CWD: b.p.CWD}
	}
	if b.meta.Ref != "" {
		e.Raw = &model.RawRef{Ref: b.meta.Ref}
	}
	return e
}

func (b builder) one(kind model.Kind, data map[string]any) []model.Event {
	return []model.Event{b.base(kind, data)}
}

// titled is a session.update with OpenCode's own session title.
func (b builder) titled() model.Event {
	return b.base(model.KindSessionUpdate, compact(map[string]any{"title": b.p.Title, "title_source": "agent"}))
}

// subagent is a subagent.start/end for this (child) session, recorded on
// its parent.
func (b builder) subagent(kind model.Kind, status string) model.Event {
	e := b.base(kind, compact(map[string]any{"child_session_id": b.actor, "agent_type": b.agentType(), "status": status}))
	e.ActorID, e.ParentActor, e.ActorType = b.parent, "", ""
	if i := strings.LastIndex(b.parent, "/sub:"); i > 0 {
		e.ParentActor = b.parent[:i]
	}
	if kind == model.KindSubagentStart {
		e.DedupKey = b.actor + ":subagent.start"
	}
	return e
}

var subagentTitle = regexp.MustCompile(`\(@([\w.-]+) subagent\)\s*$`)

// agentType is the agent a session runs as: the one the payload names,
// else the one in OpenCode's child session title "<task> (@<agent> subagent)".
func (b builder) agentType() string {
	if b.p.Agent != "" {
		return b.p.Agent
	}
	if m := subagentTitle.FindStringSubmatch(b.p.Title); m != nil {
		return m[1]
	}
	return ""
}

// usage is one completed assistant message: OpenCode's own token counts
// and cost. OpenCode reports input without cached tokens and output
// without reasoning tokens; reasoning is billed as output.
func (b builder) usage() model.Event {
	p, t := b.p, b.p.Tokens
	ts := msTime(p.CompletedAt, b.ts)
	d := compact(map[string]any{
		"model": p.ModelID, "provider": p.ProviderID, "message_id": p.MessageID,
		"input_tokens": t.Input, "output_tokens": t.Output + t.Reasoning, "reasoning_tokens": t.Reasoning,
		"cache_read_tokens": t.Cache.Read, "cache_write_tokens": t.Cache.Write, "finish": p.Finish,
	})
	switch {
	case p.Cost != nil && *p.Cost > 0:
		d["cost_usd"], d["cost_source"] = *p.Cost, "reported"
	default:
		u := pricing.Usage{Input: t.Input, Output: t.Output + t.Reasoning, CacheRead: t.Cache.Read, CacheWrite5m: t.Cache.Write, At: ts}
		if cost, ok := pricing.Default().Cost(p.ModelID, u); ok {
			d["cost_usd"], d["cost_source"] = cost, "computed"
		} else {
			d["cost_source"] = "unpriced"
		}
	}
	e := b.base(model.KindUsage, d)
	e.TS, e.ID = ts, model.NewULID(ts)
	e.DedupKey = b.actor + ":usage:" + p.MessageID
	return e
}

// tool maps a tool part. A running part is tool.start; a finished one is
// tool.end plus tool.start (deduplicated against the running one, and
// needed when the tool finished before OpenCode published it running).
func (b builder) tool() []model.Event {
	p := b.p
	if p.CallID == "" {
		return nil
	}
	var in toolInput
	_ = json.Unmarshal(p.Input, &in)
	tool := NormalizeTool(p.Tool)
	start := msTime(p.StartedAt, b.ts)
	at := func(e model.Event, ts time.Time) model.Event {
		e.TS, e.ID = ts, model.NewULID(ts)
		return e
	}
	s := at(b.base(model.KindToolStart, compact(map[string]any{
		"tool_call_id": p.CallID, "tool": tool, "tool_raw": p.Tool, "input_summary": summarize(in),
	})), start)
	s.DedupKey = b.actor + ":" + p.CallID + ":start"
	if p.Status == "running" {
		return []model.Event{s}
	}
	end := msTime(p.EndedAt, b.ts)
	ok := p.Status == "completed"
	d := map[string]any{"tool_call_id": p.CallID, "tool": tool, "ok": ok}
	if p.StartedAt > 0 && p.EndedAt >= p.StartedAt {
		d["duration_ms"] = p.EndedAt - p.StartedAt
	}
	var errText string
	_ = json.Unmarshal(p.Error, &errText)
	if errText != "" {
		d["error"] = errText
	}
	e := at(b.base(model.KindToolEnd, d), end)
	e.DedupKey = b.actor + ":" + p.CallID + ":end"
	out := []model.Event{s, e}
	derived := func(kind model.Kind, suffix string, d map[string]any) {
		x := at(b.base(kind, d), end)
		x.DedupKey = b.actor + ":" + p.CallID + ":" + suffix
		out = append(out, x)
	}
	switch tool {
	case model.ToolShell:
		d := compact(map[string]any{"command": in.Command, "tool_call_id": p.CallID, "cwd": first(in.Workdir, p.CWD)})
		if p.ExitCode != nil {
			d["exit_code"] = *p.ExitCode
		}
		if v, ok := e.Data["duration_ms"]; ok {
			d["duration_ms"] = v
		}
		derived(model.KindShellExec, "shell", d)
	case model.ToolRead:
		if f := first(in.FilePath, in.Path); f != "" && ok {
			derived(model.KindFileRead, "file", map[string]any{"path": joinPath(p.CWD, f)})
		}
	case model.ToolEdit, model.ToolWrite:
		if !ok {
			break
		}
		for i, d := range b.edits(in) {
			derived(model.KindFileEdit, "file:"+strconv.Itoa(i), d)
		}
	}
	return out
}

// edits are the file.edit events of a finished edit, write or apply_patch
// call. Line counts and diffs come from OpenCode's tool metadata when it
// has them.
func (b builder) edits(in toolInput) []map[string]any {
	p := b.p
	switch p.Tool {
	case "apply_patch", "patch":
		var out []map[string]any
		for i, f := range p.Files {
			op := map[string]string{"add": "create", "delete": "delete", "move": "move"}[f.Type]
			d := map[string]any{"path": joinPath(p.CWD, first(f.MovePath, f.Path)), "op": first(op, "modify")}
			if f.MovePath != "" {
				d["from"] = joinPath(p.CWD, f.Path)
			}
			lineCounts(d, f)
			if i < len(p.FilePatches) {
				if h := hunks(p.FilePatches[i]); h != "" {
					d["patch"], d["patch_source"] = h, "agent"
				}
			}
			out = append(out, d)
		}
		return out
	case "edit":
		f := first(in.FilePath, in.Path)
		if p.FileDiff != nil {
			f = first(p.FileDiff.File, f)
		}
		if f == "" {
			return nil
		}
		d := map[string]any{"path": joinPath(p.CWD, f), "op": "modify"}
		if p.FileDiff != nil && p.FileDiff.Additions != nil {
			lineCounts(d, *p.FileDiff)
		} else if in.OldString != "" || in.NewString != "" {
			a, r := lineDiff(in.OldString, in.NewString)
			d["lines_added"], d["lines_removed"], d["lines_source"] = a, r, "computed"
		}
		if h := hunks(p.Diff); h != "" {
			d["patch"], d["patch_source"] = h, "agent"
		} else if in.OldString != "" || in.NewString != "" {
			d["patch"], d["patch_source"] = adapters.SnippetPatch([2]string{in.OldString, in.NewString}), "computed"
		}
		return []map[string]any{d}
	case "write":
		f := first(in.FilePath, in.Path)
		if f == "" {
			return nil
		}
		d := map[string]any{"path": joinPath(p.CWD, f), "op": "modify"}
		if p.Exists != nil && !*p.Exists {
			d["op"] = "create"
			d["lines_added"], d["lines_removed"], d["lines_source"] = len(splitLines(in.Content)), 0, "computed"
		}
		return []map[string]any{d}
	}
	return nil
}

func lineCounts(d map[string]any, f fileChange) {
	if f.Additions != nil && f.Deletions != nil {
		d["lines_added"], d["lines_removed"], d["lines_source"] = *f.Additions, *f.Deletions, "agent"
	}
}

// hunks keeps the "@@" hunks of a unified diff, dropping file headers
// (Index:, ===, ---, +++).
func hunks(diff string) string {
	i := strings.Index(diff, "@@")
	if i < 0 {
		return ""
	}
	if i > 0 && diff[i-1] != '\n' {
		return ""
	}
	h := diff[i:]
	if !strings.HasSuffix(h, "\n") {
		h += "\n"
	}
	return h
}

// NormalizeTool maps OpenCode tool ids to normalized tool names.
func NormalizeTool(name string) string {
	switch name {
	case "bash", "shell":
		return model.ToolShell
	case "read", "list", "lsp":
		return model.ToolRead
	case "write":
		return model.ToolWrite
	case "edit", "apply_patch", "patch", "multiedit":
		return model.ToolEdit
	case "grep", "glob", "codesearch":
		return model.ToolSearch
	case "webfetch", "websearch":
		return model.ToolWeb
	case "task":
		return model.ToolTask
	}
	return model.ToolOther
}

// summarize is a short description of a tool call for the board.
func summarize(in toolInput) string {
	s := first(in.Command, in.FilePath, in.Path, in.Pattern, in.URL, in.Query, in.Desc)
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200])
	}
	return s
}

type errInfo struct {
	Name    string `json:"name"`
	Message string `json:"message"`
}

func errorOf(raw json.RawMessage) errInfo {
	var e errInfo
	if json.Unmarshal(raw, &e) != nil {
		_ = json.Unmarshal(raw, &e.Message)
	}
	return e
}

func msTime(ms int64, fallback time.Time) time.Time {
	if ms > 0 {
		return time.UnixMilli(ms).UTC()
	}
	return fallback.UTC()
}

// lineDiff counts changed lines between two snippets, ignoring the lines
// they share at the start and end.
func lineDiff(old, new string) (added, removed int) {
	a, b := splitLines(old), splitLines(new)
	for len(a) > 0 && len(b) > 0 && a[0] == b[0] {
		a, b = a[1:], b[1:]
	}
	for len(a) > 0 && len(b) > 0 && a[len(a)-1] == b[len(b)-1] {
		a, b = a[:len(a)-1], b[:len(b)-1]
	}
	return len(b), len(a)
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// joinPath resolves p against cwd in the agent's own path style.
func joinPath(cwd, p string) string {
	if cwd == "" || p == "" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\\`) || (len(p) > 2 && p[1] == ':' && (p[2] == '\\' || p[2] == '/')) {
		return p
	}
	if strings.HasPrefix(cwd, "/") {
		return path.Join(cwd, p)
	}
	return strings.TrimRight(cwd, `\/`) + `\` + strings.ReplaceAll(p, "/", `\`)
}

func compact(m map[string]any) map[string]any {
	for k, v := range m {
		if s, ok := v.(string); ok && s == "" {
			delete(m, k)
		}
	}
	return m
}

func first(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
