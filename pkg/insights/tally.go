// Package insights computes failure analytics from metadata only: tool
// names, outcomes, exit codes, program names, file tool paths, ids and
// times. No prompt, command, output or error text is read, so the same
// numbers can be built from synced data, where none of that exists.
package insights

import (
	"cmp"
	"slices"
	"time"

	"github.com/elephaant/shiplino/pkg/model"
	"github.com/elephaant/shiplino/pkg/redact"
)

// LoopMin is how many failures in a row make a retry loop.
const LoopMin = 3

// Caps that keep a tally small whatever an agent sends.
const (
	maxPending = 256 // calls started and not finished
	maxLoops   = 50  // finished loops kept per session
)

// Tally is one session's (or subagent's) failures, folded event by event
// by the engine.
type Tally struct {
	Tools   []ToolCount  `json:"tools,omitempty"`
	Shell   []ShellCount `json:"shell,omitempty"`
	Denials int          `json:"denials,omitempty"` // permission prompts refused (outside a tool call)
	Loops   []Loop       `json:"loops,omitempty"`
	// Ending is how the last turn ended when it went badly: "error" or
	// "interrupted" ("" otherwise). Failed sticks once the session itself
	// ended on an error.
	Ending  string    `json:"ending,omitempty"`
	EndedAt time.Time `json:"ended_at,omitzero"`
	Failed  bool      `json:"failed,omitempty"`

	// Pending are calls in flight by tool call id: started (for the tool's
	// own name and target), or a shell call that ended and waits for its
	// exit code. Runs are failure streaks by tool and target.
	Pending map[string]Pending `json:"pending,omitempty"`
	Runs    map[string]*Loop   `json:"runs,omitempty"`
}

// ToolCount is how often one tool failed or was denied.
type ToolCount struct {
	Tool     string `json:"tool"`     // normalized: edit, shell, …
	ToolRaw  string `json:"tool_raw"` // the agent's own name
	Failures int    `json:"failures,omitempty"`
	Denials  int    `json:"denials,omitempty"`
}

// ShellCount is how often one program exited with one non-zero code.
type ShellCount struct {
	Program  string `json:"program"` // "" when it couldn't be told
	ExitCode int    `json:"exit_code"`
	Failures int    `json:"failures"`
}

// Loop is the same tool failing on the same target LoopMin or more times
// with no success in between. The target is a file tool's path or a shell
// command's program.
type Loop struct {
	Tool     string    `json:"tool"`
	ToolRaw  string    `json:"tool_raw"`
	Target   string    `json:"target"`
	Failures int       `json:"failures"`
	First    time.Time `json:"first"`
	Last     time.Time `json:"last"`
}

// Pending is a call in flight.
type Pending struct {
	ToolRaw string `json:"tool_raw,omitempty"`
	Target  string `json:"target,omitempty"`
	Ended   bool   `json:"ended,omitempty"` // a shell call waiting for its exit code
	OK      bool   `json:"ok,omitempty"`
}

// pathTools are tools whose input summary is a file path.
var pathTools = map[string]bool{model.ToolRead: true, model.ToolEdit: true, model.ToolWrite: true}

// Empty reports whether the tally holds nothing.
func (t *Tally) Empty() bool {
	return t == nil || len(t.Tools) == 0 && len(t.Shell) == 0 && t.Denials == 0 && len(t.Loops) == 0 &&
		t.Ending == "" && !t.Failed && len(t.Pending) == 0 && len(t.Runs) == 0
}

// Fold adds one event of this session.
func (t *Tally) Fold(e model.Event) {
	d := e.Data
	id := str(d, "tool_call_id")
	switch e.Kind {
	case model.KindToolStart:
		if id == "" {
			return
		}
		if t.Pending == nil || len(t.Pending) >= maxPending {
			t.Pending = map[string]Pending{} // ends were lost; start over
		}
		p := Pending{ToolRaw: str(d, "tool_raw")}
		if pathTools[str(d, "tool")] {
			p.Target = str(d, "input_summary")
		}
		t.Pending[id] = p

	case model.KindToolEnd:
		p := t.Pending[id]
		delete(t.Pending, id)
		tool := str(d, "tool")
		raw := cmp.Or(str(d, "tool_raw"), p.ToolRaw, tool)
		ok := boolean(d, "ok", true)
		switch {
		case boolean(d, "denied", false):
			t.tool(tool, raw).Denials++
			t.step(raw, tool, p.Target, false, e.TS)
			return
		case boolean(d, "interrupted", false):
			if !t.Failed {
				t.Ending, t.EndedAt = "interrupted", e.TS
			}
			t.step(raw, tool, p.Target, false, e.TS)
			return
		case !ok:
			t.tool(tool, raw).Failures++
		}
		if tool == model.ToolShell && id != "" {
			// The program and exit code come with the shell event.
			if t.Pending == nil {
				t.Pending = map[string]Pending{}
			}
			t.Pending[id] = Pending{ToolRaw: raw, Ended: true, OK: ok}
			return
		}
		t.step(raw, tool, p.Target, !ok, e.TS)

	case model.KindShellExec:
		p, found := t.Pending[id]
		if found && p.Ended {
			delete(t.Pending, id)
		}
		// Shell events stored before program names existed have only the
		// (local, redacted) command.
		program := cmp.Or(str(d, "program"), redact.Program(str(d, "command")))
		code, hasCode := integer(d, "exit_code")
		exited := hasCode && code != 0 && !boolean(d, "interrupted", false)
		if exited {
			t.shell(program, code).Failures++
		}
		failed := exited || found && p.Ended && !p.OK
		t.step(cmp.Or(p.ToolRaw, str(d, "tool_raw"), model.ToolShell), model.ToolShell, program, failed, e.TS)

	case model.KindWaitingEnd:
		if boolean(d, "denied", false) {
			t.Denials++
		}

	case model.KindTurnEnd:
		t.Pending = nil // in-flight calls end with the turn
		if t.Failed {
			return
		}
		switch s := str(d, "status"); s {
		case "error", "interrupted":
			t.Ending, t.EndedAt = s, e.TS
		default:
			t.Ending, t.EndedAt = "", time.Time{}
		}

	case model.KindSessionEnd:
		t.Pending = nil
		if str(d, "status") == "error" {
			t.Failed, t.Ending, t.EndedAt = true, "error", e.TS
		}
	}
}

// step advances the failure streak of a tool on a target.
func (t *Tally) step(raw, tool, target string, failed bool, at time.Time) {
	if target == "" {
		return
	}
	key := raw + "\x00" + target
	r := t.Runs[key]
	if !failed {
		if r != nil {
			if r.Failures >= LoopMin && len(t.Loops) < maxLoops {
				t.Loops = append(t.Loops, *r)
			}
			delete(t.Runs, key)
		}
		return
	}
	if r == nil {
		if t.Runs == nil {
			t.Runs = map[string]*Loop{}
		}
		r = &Loop{Tool: tool, ToolRaw: raw, Target: target, First: at}
		t.Runs[key] = r
	}
	r.Failures++
	r.Last = at
}

// AllLoops returns the finished loops and the streaks still going that
// are long enough, oldest first.
func (t *Tally) AllLoops() []Loop {
	out := slices.Clone(t.Loops)
	for _, r := range t.Runs {
		if r.Failures >= LoopMin {
			out = append(out, *r)
		}
	}
	slices.SortFunc(out, func(a, b Loop) int { return cmp.Or(a.First.Compare(b.First), cmp.Compare(a.Target, b.Target)) })
	return out
}

func (t *Tally) tool(tool, raw string) *ToolCount {
	for i := range t.Tools {
		if t.Tools[i].Tool == tool && t.Tools[i].ToolRaw == raw {
			return &t.Tools[i]
		}
	}
	t.Tools = append(t.Tools, ToolCount{Tool: tool, ToolRaw: raw})
	return &t.Tools[len(t.Tools)-1]
}

func (t *Tally) shell(program string, code int) *ShellCount {
	for i := range t.Shell {
		if t.Shell[i].Program == program && t.Shell[i].ExitCode == code {
			return &t.Shell[i]
		}
	}
	t.Shell = append(t.Shell, ShellCount{Program: program, ExitCode: code})
	return &t.Shell[len(t.Shell)-1]
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func boolean(m map[string]any, k string, def bool) bool {
	if b, ok := m[k].(bool); ok {
		return b
	}
	return def
}

// integer reads a JSON number (float64 after decoding, int when built in
// Go).
func integer(m map[string]any, k string) (int, bool) {
	switch n := m[k].(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	}
	return 0, false
}
