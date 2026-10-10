package insights

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/elephaant/shiplino/pkg/model"
)

var t0 = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

func at(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }

// fold feeds events to a new tally.
func fold(evs ...model.Event) *Tally {
	t := &Tally{}
	for _, e := range evs {
		t.Fold(e)
	}
	return t
}

func ev(kind model.Kind, m int, data map[string]any) model.Event {
	return model.Event{Kind: kind, TS: at(m), Data: data}
}

// edit is a Claude Code style call: tool_raw only on the start.
func edit(id string, m int, path string, ok bool) []model.Event {
	return []model.Event{
		ev(model.KindToolStart, m, map[string]any{"tool_call_id": id, "tool": "edit", "tool_raw": "Edit", "input_summary": path}),
		ev(model.KindToolEnd, m, map[string]any{"tool_call_id": id, "tool": "edit", "ok": ok}),
	}
}

// bash is a shell call; the exit code comes after the tool's end.
func bash(id string, m int, data map[string]any, ok bool) []model.Event {
	data["tool_call_id"] = id
	return []model.Event{
		ev(model.KindToolStart, m, map[string]any{"tool_call_id": id, "tool": "shell", "tool_raw": "Bash", "input_summary": "x"}),
		ev(model.KindToolEnd, m, map[string]any{"tool_call_id": id, "tool": "shell", "ok": ok}),
		ev(model.KindShellExec, m, data),
	}
}

func cat(parts ...[]model.Event) []model.Event {
	var out []model.Event
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func TestTallyLoops(t *testing.T) {
	tl := fold(cat(
		edit("1", 1, "/home/dev/api/a.go", false), edit("2", 2, "/home/dev/api/a.go", false),
		edit("x", 2, "/home/dev/api/b.go", true), // another target doesn't break the run
		edit("3", 3, "/home/dev/api/a.go", false), edit("4", 4, "/home/dev/api/a.go", true),
		edit("5", 5, "/home/dev/api/a.go", false), edit("6", 6, "/home/dev/api/a.go", false),
		// go fails 3 times (program from the command, as in old events), then
		// one more still going
		bash("g1", 7, map[string]any{"command": "cd api && go test ./...", "exit_code": 1}, false),
		bash("g2", 8, map[string]any{"program": "go", "exit_code": 1}, false),
		bash("g3", 9, map[string]any{"program": "go", "exit_code": 2}, false),
	)...)
	loops := tl.AllLoops()
	if len(loops) != 2 {
		t.Fatalf("loops: %+v", loops)
	}
	if l := loops[0]; l.ToolRaw != "Edit" || l.Target != "/home/dev/api/a.go" || l.Failures != 3 || !l.First.Equal(at(1)) || !l.Last.Equal(at(3)) {
		t.Errorf("edit loop: %+v", l)
	}
	if l := loops[1]; l.ToolRaw != "Bash" || l.Target != "go" || l.Failures != 3 {
		t.Errorf("go loop (still going): %+v", l)
	}
	if len(tl.Tools) != 2 || tl.Tools[0].Failures != 5 || tl.Tools[1].Failures != 3 {
		t.Errorf("tools: %+v", tl.Tools)
	}
	if len(tl.Shell) != 2 || tl.Shell[0] != (ShellCount{"go", 1, 2}) || tl.Shell[1] != (ShellCount{"go", 2, 1}) {
		t.Errorf("shell: %+v", tl.Shell)
	}
}

func TestTallyOutcomes(t *testing.T) {
	cases := []struct {
		name    string
		evs     []model.Event
		ending  string
		denials int
		tools   string
	}{
		{"turn error", []model.Event{ev(model.KindTurnEnd, 1, map[string]any{"status": "error"})}, "error", 0, "[]"},
		{"turn ok after an interrupt", []model.Event{
			ev(model.KindTurnEnd, 1, map[string]any{"status": "interrupted"}), ev(model.KindTurnEnd, 2, map[string]any{"status": "ok"})}, "", 0, "[]"},
		{"interrupted tool call", cat(
			[]model.Event{ev(model.KindTurnEnd, 1, map[string]any{"status": "ok"})},
			[]model.Event{ev(model.KindToolEnd, 2, map[string]any{"tool_call_id": "i", "tool": "shell", "ok": false, "interrupted": true})},
		), "interrupted", 0, "[]"},
		{"session error sticks", []model.Event{
			ev(model.KindSessionEnd, 1, map[string]any{"status": "error"}), ev(model.KindTurnEnd, 2, map[string]any{"status": "ok"})}, "error", 0, "[]"},
		{"denied call (Claude Code PermissionDenied)", []model.Event{
			ev(model.KindToolStart, 1, map[string]any{"tool_call_id": "d", "tool": "shell", "tool_raw": "Bash"}),
			ev(model.KindToolEnd, 1, map[string]any{"tool_call_id": "d", "tool": "shell", "ok": false, "denied": true})},
			"", 0, `[{"tool":"shell","tool_raw":"Bash","denials":1}]`},
		{"refused permission (OpenCode reject)", []model.Event{
			ev(model.KindWaitingEnd, 1, map[string]any{"resolution": "reject", "denied": true})}, "", 1, "[]"},
		{"tool name from the end event (Codex)", []model.Event{
			ev(model.KindToolEnd, 1, map[string]any{"tool_call_id": "c", "tool": "mcp", "tool_raw": "mcp__db__query", "ok": false})},
			"", 0, `[{"tool":"mcp","tool_raw":"mcp__db__query","failures":1}]`},
		{"wrapped command without a tool call", []model.Event{
			ev(model.KindShellExec, 1, map[string]any{"program": "make", "exit_code": 2})}, "", 0, "[]"},
	}
	for _, c := range cases {
		tl := fold(c.evs...)
		tools, _ := json.Marshal(tl.Tools)
		if tools == nil || string(tools) == "null" {
			tools = []byte("[]")
		}
		if tl.Ending != c.ending || tl.Denials != c.denials || string(tools) != c.tools {
			t.Errorf("%s: ending %q denials %d tools %s", c.name, tl.Ending, tl.Denials, tools)
		}
	}
}

func TestTallyStaysSmall(t *testing.T) {
	tl := &Tally{}
	for i := range 1000 {
		tl.Fold(ev(model.KindToolStart, i, map[string]any{"tool_call_id": fmt.Sprint(i), "tool": "read"}))
	}
	if len(tl.Pending) > maxPending {
		t.Errorf("pending: %d", len(tl.Pending))
	}
	tl.Fold(ev(model.KindTurnEnd, 2000, map[string]any{"status": "ok"}))
	if !tl.Empty() {
		t.Errorf("not empty after the turn: %+v", tl)
	}
}

func TestReport(t *testing.T) {
	s1 := fold(cat(
		edit("1", 1, "src/a.go", false), edit("2", 2, "src/a.go", false), edit("3", 3, "src/a.go", false),
		bash("b", 4, map[string]any{"program": "go", "exit_code": 1}, false),
		[]model.Event{ev(model.KindTurnEnd, 5, map[string]any{"status": "error"})},
	)...)
	sub := fold(bash("n", 2, map[string]any{"program": "npm", "exit_code": 1}, false)...)
	sub.Fold(ev(model.KindTurnEnd, 3, map[string]any{"status": "interrupted"})) // a subagent's ending doesn't count
	s2 := fold(cat(
		bash("g", 1, map[string]any{"program": "go", "exit_code": 1}, false),
		[]model.Event{ev(model.KindWaitingEnd, 2, map[string]any{"denied": true})},
	)...)
	f := Report([]Entry{
		{Root: "cc:1", Agent: "claude-code", Project: "p1", IsRoot: true, At: at(5), Tally: s1},
		{Root: "cc:1", Agent: "claude-code", Project: "p1", At: at(3), Tally: sub},
		{Root: "cx:2", Agent: "codex", Project: "p2", IsRoot: true, At: at(9), Tally: s2},
		{Root: "cc:3", Agent: "claude-code", Project: "p1", IsRoot: true, At: at(9), Tally: nil},
	})
	if f.ToolFailures != 6 || f.ShellFailures != 3 || f.Denials != 1 || f.RetryLoops != 1 || f.EndedBadly != 1 {
		t.Fatalf("totals: %+v", f)
	}
	if r := f.Shell[0]; r.Program != "go" || r.Failures != 2 || r.Count != 2 || r.IDs[0] != "cx:2" || len(r.Agents) != 2 {
		t.Errorf("go row: %+v", r)
	}
	if r := f.Tools[0]; r.Agent != "claude-code" || r.ToolRaw != "Edit" || r.Failures != 3 || r.Projects[0] != "p1" {
		t.Errorf("top tool: %+v", r)
	}
	if l := f.Loops[0]; l.Session != "cc:1" || l.Target != "src/a.go" || l.Failures != 3 {
		t.Errorf("loop: %+v", l)
	}
	if e := f.Endings[0]; len(f.Endings) != 1 || e.Session != "cc:1" || e.Status != "error" || !e.At.Equal(at(5)) {
		t.Errorf("endings: %+v", f.Endings)
	}
	if p := f.Projects[0]; p.Key != "p1" || p.ToolFailures != 5 || p.ShellFailures != 2 || p.RetryLoops != 1 || p.EndedBadly != 1 || p.Count != 1 {
		t.Errorf("project p1: %+v", p)
	}
}

func TestReportEmpty(t *testing.T) {
	b, _ := json.Marshal(Report(nil))
	want := `{"tool_failures":0,"shell_failures":0,"denials":0,"retry_loops":0,"ended_badly":0,"tools":[],"shell":[],"loops":[],"endings":[],"agents":[],"projects":[]}`
	if string(b) != want {
		t.Errorf("got %s", b)
	}
}
