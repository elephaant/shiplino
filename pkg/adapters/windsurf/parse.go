package windsurf

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/elephaant/shiplino/pkg/adapters"
	"github.com/elephaant/shiplino/pkg/model"
)

// Name is the adapter name used in `shiplino hook --agent windsurf`.
const Name = "windsurf"

func init() { adapters.Register(Adapter{}) }

// Adapter is the Windsurf adapter.
type Adapter struct{}

// Name implements adapters.Adapter.
func (Adapter) Name() string { return Name }

type payload struct {
	Action       string `json:"agent_action_name"`
	TrajectoryID string `json:"trajectory_id"`
	ExecutionID  string `json:"execution_id"`
	Timestamp    string `json:"timestamp"`
	ModelName    string `json:"model_name"`
	ToolInfo     struct {
		FilePath   string `json:"file_path"`
		UserPrompt string `json:"user_prompt"`
		Edits      []struct {
			Old string `json:"old_string"`
			New string `json:"new_string"`
		} `json:"edits"`
		CommandLine string `json:"command_line"`
		CWD         string `json:"cwd"`
		MCPServer   string `json:"mcp_server_name"`
		MCPTool     string `json:"mcp_tool_name"`
	} `json:"tool_info"`
}

// ParseHook implements adapters.Adapter.
func (Adapter) ParseHook(raw []byte, meta adapters.HookMeta) ([]model.Event, error) {
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("windsurf: decode hook payload: %w", err)
	}
	if p.TrajectoryID == "" {
		return nil, fmt.Errorf("windsurf: hook payload has no trajectory_id")
	}
	b := builder{p: &p, meta: meta, sid: model.SessionID(Name, p.TrajectoryID), ts: meta.ReceivedAt.UTC()}
	// Windsurf's own timestamp wins over the time the hook ran.
	if t, err := time.Parse(time.RFC3339Nano, p.Timestamp); err == nil {
		b.ts = t.UTC()
	}
	ti := &p.ToolInfo

	switch event := first(p.Action, meta.Event); event {
	case "pre_user_prompt":
		e := b.base(model.KindTurnStart, map[string]any{"prompt": ti.UserPrompt, "prompt_chars": len([]rune(ti.UserPrompt))})
		b.keyed(&e, "turn:"+p.ExecutionID, p.ExecutionID)
		out := []model.Event{e}
		if m := p.ModelName; m != "" && m != "Unknown" {
			out = append(out, b.base(model.KindSessionUpdate, map[string]any{"model": m}))
		}
		return out, nil
	case "post_cascade_response":
		e := b.base(model.KindTurnEnd, map[string]any{"status": "ok"})
		b.keyed(&e, "turnend:"+p.ExecutionID, p.ExecutionID)
		return []model.Event{e}, nil
	case "post_read_code":
		if ti.FilePath == "" {
			return nil, nil
		}
		b.cwd = dir(ti.FilePath)
		return append(b.tool(model.ToolRead, "read_code", ti.FilePath),
			b.base(model.KindFileRead, map[string]any{"path": ti.FilePath, "op": "read"})), nil
	case "post_write_code":
		if ti.FilePath == "" {
			return nil, nil
		}
		b.cwd = dir(ti.FilePath)
		added, removed := 0, 0
		for _, ed := range ti.Edits {
			a, r := lineDiff(ed.Old, ed.New)
			added, removed = added+a, removed+r
		}
		d := map[string]any{"path": ti.FilePath, "op": "modify"}
		if len(ti.Edits) > 0 {
			d["lines_added"], d["lines_removed"], d["lines_source"] = added, removed, "computed"
		}
		return append(b.tool(model.ToolEdit, "write_code", ti.FilePath), b.base(model.KindFileEdit, d)), nil
	case "post_run_command":
		b.cwd = ti.CWD
		// Windsurf reports neither the exit code nor the duration.
		return append(b.tool(model.ToolShell, "run_command", ti.CommandLine),
			b.base(model.KindShellExec, compact(map[string]any{"command": ti.CommandLine, "cwd": ti.CWD, "tool_call_id": b.callID()}))), nil
	case "post_mcp_tool_use":
		raw := "mcp:" + ti.MCPServer + "/" + ti.MCPTool
		return append(b.tool(model.ToolMCP, raw, ti.MCPTool),
			b.base(model.KindMCPCall, compact(map[string]any{"server": ti.MCPServer, "tool": ti.MCPTool, "ok": true}))), nil
	default:
		return nil, fmt.Errorf("%w: windsurf %q", adapters.ErrUnknownEvent, event)
	}
}

type builder struct {
	p    *payload
	meta adapters.HookMeta
	sid  string
	ts   time.Time
	cwd  string
}

func (b builder) base(kind model.Kind, data map[string]any) model.Event {
	e := model.Event{
		ID: model.NewULID(b.meta.ReceivedAt), V: model.SchemaVersion, TS: b.ts, ReceivedAt: b.meta.ReceivedAt.UTC(),
		Kind: kind, Agent: model.Agent{Name: Name}, Collector: model.CollectorHook,
		MachineID: b.meta.MachineID, User: b.meta.User, SessionID: b.sid, ActorID: b.sid, TurnID: b.p.ExecutionID, Data: data,
		DedupKey: b.sid + ":" + string(kind) + ":" + b.meta.EnvelopeID,
	}
	if b.cwd != "" {
		e.Project = &model.Project{CWD: b.cwd}
	}
	if b.meta.Ref != "" {
		e.Raw = &model.RawRef{Ref: b.meta.Ref}
	}
	return e
}

// keyed gives e a stable dedup key when the agent supplied an id.
func (b builder) keyed(e *model.Event, key, id string) {
	if id != "" {
		e.DedupKey = b.sid + ":" + key
	}
}

// callID identifies a tool call. Windsurf has no tool call ids and each
// post hook is one finished call, so the spool line's id is used.
func (b builder) callID() string { return "ws-" + b.meta.EnvelopeID }

// tool records a finished call as a start/end pair at the same instant:
// post hooks carry no duration.
func (b builder) tool(tool, rawName, summary string) []model.Event {
	id := b.callID()
	if len(summary) > 200 {
		summary = summary[:200]
	}
	start := b.base(model.KindToolStart, compact(map[string]any{"tool_call_id": id, "tool": tool, "tool_raw": rawName, "input_summary": summary}))
	end := b.base(model.KindToolEnd, map[string]any{"tool_call_id": id, "tool": tool, "ok": true})
	start.DedupKey, end.DedupKey = b.sid+":"+id+":start", b.sid+":"+id+":end"
	return []model.Event{start, end}
}

// dir is the folder of a file path in the agent's own path style, used as
// the working directory when the payload has none.
func dir(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i > 0 {
		if strings.HasPrefix(p, "/") {
			return path.Clean(p[:i])
		}
		return p[:i]
	}
	return ""
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
