package geminicli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/elephaant/shiplino/pkg/adapters"
	"github.com/elephaant/shiplino/pkg/model"
)

var update = flag.Bool("update", false, "rewrite golden files")

var t0 = time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)

const testdata = "0.63"

func golden(t *testing.T, name string, events []model.Event) {
	t.Helper()
	for i := range events {
		events[i].ID = ""
		events[i].Raw = nil // holds temp paths
	}
	b, _ := json.MarshalIndent(events, "", "  ")
	path := filepath.Join("testdata", testdata, name)
	if *update {
		os.WriteFile(path, append(b, '\n'), 0o644)
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update)", err)
	}
	if !bytes.Equal(bytes.TrimSpace(bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))), bytes.TrimSpace(b)) {
		t.Errorf("events differ from %s; run with -update and review the diff", path)
	}
}

func lines(t *testing.T, name string) [][]byte {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", testdata, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out [][]byte
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		out = append(out, append([]byte(nil), sc.Bytes()...))
	}
	return out
}

func validate(t *testing.T, i int, evs []model.Event) {
	t.Helper()
	for _, e := range evs {
		if err := e.Validate(); err != nil {
			t.Fatalf("line %d: %v", i+1, err)
		}
		if e.DedupKey == "" {
			t.Fatalf("line %d: %s without dedup key", i+1, e.Kind)
		}
	}
}

func byKind(evs []model.Event) map[model.Kind][]model.Event {
	m := map[model.Kind][]model.Event{}
	for _, e := range evs {
		m[e.Kind] = append(m[e.Kind], e)
	}
	return m
}

func TestHooksGolden(t *testing.T) {
	var all []model.Event
	for i, l := range lines(t, "hooks.jsonl") {
		evs, err := Adapter{}.ParseHook(l, adapters.HookMeta{EnvelopeID: fmt.Sprintf("env-%02d", i), ReceivedAt: t0.Add(time.Hour)})
		if err != nil {
			t.Fatalf("line %d: %v", i+1, err)
		}
		validate(t, i, evs)
		all = append(all, evs...)
	}
	golden(t, "hooks.golden.json", all)

	k := byKind(all)
	if all[0].SessionID != "gemini-cli:gs1a2b3c-0000-4000-8000-000000000001" || !all[0].TS.Equal(t0) {
		t.Errorf("first event: session %s at %s (the payload's own timestamp wins)", all[0].SessionID, all[0].TS)
	}
	if sh := k[model.KindShellExec]; len(sh) != 1 || sh[0].Data["command"] != "rg -n health src" || sh[0].Data["exit_code"] != 1 || sh[0].Data["cwd"] != "/home/dev/demo" {
		t.Errorf("shell: %+v", sh)
	}
	ends := k[model.KindToolEnd]
	if len(ends) != 8 || ends[0].Data["ok"] != false || ends[1].Data["ok"] != true || ends[4].Data["ok"] != false || ends[7].Data["ok"] != false {
		t.Errorf("tool ends: %+v", ends)
	}
	// Only the write_todos call that succeeded is a plan; cancelled items
	// don't count, blocked ones do.
	if pl := k[model.KindSessionUpdate]; len(pl) != 1 || pl[0].Data["plan_total"] != 3 || pl[0].Data["plan_done"] != 1 {
		t.Errorf("plan: %+v", pl)
	}
	edits := k[model.KindFileEdit]
	if len(edits) != 2 ||
		edits[0].Data["path"] != "/home/dev/demo/src/health.ts" || edits[0].Data["op"] != "create" || edits[0].Data["lines_added"] != 1 ||
		edits[1].Data["path"] != "/home/dev/demo/src/app.ts" || edits[1].Data["lines_added"] != 2 || edits[1].Data["lines_removed"] != 1 {
		t.Errorf("edits: %+v", edits)
	}
	if r := k[model.KindFileRead]; len(r) != 1 || r[0].Data["path"] != "/home/dev/demo/README.md" {
		t.Errorf("reads (a failed read is not one): %+v", r)
	}
	if m := k[model.KindMCPCall]; len(m) != 1 || m[0].Data["server"] != "tracker" || m[0].Data["tool"] != "list_issues" {
		t.Errorf("mcp: %+v", m)
	}
	if w := k[model.KindWaitingStart]; len(w) != 1 || w[0].Data["reason"] != "permission" {
		t.Errorf("waiting: %+v", w)
	}
	if ts := k[model.KindTurnStart]; len(ts) != 1 || ts[0].Data["transcript_path"] == nil {
		t.Errorf("turn start must carry the transcript path: %+v", ts)
	}
	if te := k[model.KindTurnEnd]; len(te) != 1 || te[0].Data["assistant_summary"] != "Added a /health endpoint." {
		t.Errorf("turn end: %+v", te)
	}
	if len(k[model.KindCompact]) != 1 || len(k[model.KindSessionEnd]) != 1 {
		t.Error("compact or session end missing")
	}
}

func TestEveryRegisteredEventParses(t *testing.T) {
	for _, ev := range Events {
		raw := fmt.Sprintf(`{"session_id":"s","cwd":"/home/dev/demo","hook_event_name":%q,"notification_type":"ToolPermission"}`, ev)
		if _, err := (Adapter{}).ParseHook([]byte(raw), adapters.HookMeta{EnvelopeID: "e", ReceivedAt: t0}); err != nil {
			t.Errorf("%s: %v", ev, err)
		}
	}
	if _, err := (Adapter{}).ParseHook([]byte(`{"session_id":"s","hook_event_name":"Brand New"}`), adapters.HookMeta{}); err == nil {
		t.Error("unknown event accepted")
	}
	if _, err := (Adapter{}).ParseHook([]byte(`{"hook_event_name":"SessionStart"}`), adapters.HookMeta{}); err == nil {
		t.Error("payload without session accepted")
	}
}

// chatDir lays out ~/.gemini/tmp/demo like Gemini CLI does, so the parser
// can find the project root next to the chats.
func chatDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".gemini", "tmp", "demo")
	os.MkdirAll(filepath.Join(dir, "chats"), 0o700)
	os.WriteFile(filepath.Join(dir, ".project_root"), []byte("/home/dev/demo\n"), 0o600)
	return dir
}

func parseFile(t *testing.T, name, path string) []model.Event {
	t.Helper()
	st := map[string]string{}
	var all []model.Event
	for i, l := range lines(t, name) {
		evs, err := Adapter{}.ParseTranscriptLine(l, adapters.TranscriptMeta{ReceivedAt: t0, State: st, Ref: fmt.Sprintf("transcript:%s#%d", path, i*100)})
		if err != nil {
			t.Fatalf("line %d: %v", i+1, err)
		}
		validate(t, i, evs)
		all = append(all, evs...)
	}
	return all
}

func TestChatGolden(t *testing.T) {
	dir := chatDir(t)
	all := parseFile(t, "chat.jsonl", filepath.Join(dir, "chats", "session-2026-10-09T10-00-gs1a2b3c.jsonl"))
	golden(t, "chat.golden.json", all)

	k := byKind(all)
	if s := k[model.KindSessionStart]; len(s) != 1 || s[0].Project == nil || s[0].Project.CWD != "/home/dev/demo" || !s[0].TS.Equal(t0) {
		t.Fatalf("session start (project root from .project_root): %+v", s)
	}
	// m2 is appended three times and m3 once; usage is keyed by message id.
	keys := map[string]bool{}
	var usage []model.Event
	for _, e := range k[model.KindUsage] {
		if !keys[e.DedupKey] {
			keys[e.DedupKey] = true
			usage = append(usage, e)
		}
	}
	if len(usage) != 4 {
		t.Fatalf("usage events: %d", len(usage))
	}
	u := usage[0].Data
	if u["model"] != "gemini-3.8-flash" || u["input_tokens"] != int64(4000) || u["cache_read_tokens"] != int64(8000) || u["output_tokens"] != int64(200) || u["reasoning_tokens"] != int64(120) {
		t.Errorf("usage m2: %+v", u)
	}
	// 4000×0.75 + 8000×0.075 + 200×3.75 per million.
	if c, _ := u["cost_usd"].(float64); c < 0.004349 || c > 0.004351 || u["cost_source"] != "computed" {
		t.Errorf("cost m2: %v", u["cost_usd"])
	}
	// gemini-2.5-pro over 200K prompt tokens: long-context rates; tool-use prompt tokens bill as input.
	if u := usage[2].Data; u["input_tokens"] != int64(250005) || u["output_tokens"] != int64(50) {
		t.Errorf("usage m4: %+v", u)
	} else if c, _ := u["cost_usd"].(float64); c < 0.625762 || c > 0.625763 {
		t.Errorf("cost m4: %v", c)
	}
	if usage[3].Data["cost_source"] != "unpriced" {
		t.Errorf("unknown model priced: %+v", usage[3].Data)
	}
	if ti := k[model.KindSessionUpdate]; len(ti) != 2 || ti[0].Data["title"] != "Add a health endpoint" ||
		ti[1].Data["plan_total"] != 2 || ti[1].Data["plan_done"] != 1 {
		t.Errorf("title and plan: %+v", ti)
	}
	// The tool call is recorded when it finishes (twice, same keys); the
	// cancelled replace ends not ok and edits nothing.
	tools := map[string]bool{}
	for _, e := range k[model.KindToolEnd] {
		tools[e.DedupKey] = e.Data["ok"].(bool)
	}
	if len(tools) != 4 || tools["gemini-cli:gs1a2b3c-0000-4000-8000-000000000001:tool:replace_1760004007000_0:end"] {
		t.Errorf("tool ends: %v", tools)
	}
	if sh := k[model.KindShellExec]; len(sh) == 0 || sh[0].Data["exit_code"] != 1 || sh[0].Data["tool_call_id"] != "run_shell_command_1760004001000_0" {
		t.Errorf("shell: %+v", sh)
	}
	if ed := k[model.KindFileEdit]; len(ed) != 1 || ed[0].Data["lines_added"] != 1 || ed[0].Data["op"] != "create" {
		t.Errorf("edits: %+v", ed)
	}
	if ts := k[model.KindTurnStart]; len(ts) != 1 || ts[0].Data["prompt"] != "add a health endpoint" {
		t.Errorf("turns: %+v", ts)
	}
}

// Usage is priced at the response's own time, so a dated price change
// applies to responses after it even when they're read before or after.
func TestUsagePricedAtResponseTime(t *testing.T) {
	ref := "transcript:" + filepath.Join(chatDir(t), "chats", "session-x.jsonl") + "#1"
	cost := func(ts string) float64 {
		st := map[string]string{}
		meta := adapters.TranscriptMeta{ReceivedAt: t0, State: st, Ref: ref}
		Adapter{}.ParseTranscriptLine([]byte(`{"sessionId":"g1","projectHash":"demo","startTime":"2026-10-09T10:00:00Z"}`), meta)
		evs, _ := Adapter{}.ParseTranscriptLine([]byte(`{"id":"m1","timestamp":"`+ts+`","type":"gemini","content":"","model":"gemini-3.8-flash",
			"tokens":{"input":1000000,"output":1000000,"cached":0,"thoughts":0,"tool":0,"total":2000000}}`), meta)
		c, _ := evs[0].Data["cost_usd"].(float64)
		return c
	}
	if before, after := cost("2026-12-31T23:00:00Z"), cost("2027-01-01T01:00:00Z"); before != 4.5 || after != 9 {
		t.Fatalf("before %v, after %v", before, after)
	}
}

func TestSubagentChat(t *testing.T) {
	dir := chatDir(t)
	parent := "gs1a2b3c-0000-4000-8000-000000000001"
	all := parseFile(t, "subagent.jsonl", filepath.Join(dir, "chats", parent, "sub-77.jsonl"))
	if len(all) != 2 {
		t.Fatalf("events: %+v", all)
	}
	for _, e := range all {
		if e.SessionID != "gemini-cli:"+parent || e.ActorID != "gemini-cli:"+parent+"/sub:sub-77" || e.ParentActor != e.SessionID || e.Project == nil {
			t.Errorf("attribution: %+v", e)
		}
	}
	if u := all[1]; u.Kind != model.KindUsage || u.Data["cost_source"] != "computed" {
		t.Errorf("usage: %+v", u)
	}
}

func TestWarmupRebuildsStateWithoutEvents(t *testing.T) {
	dir := chatDir(t)
	ref := "transcript:" + filepath.Join(dir, "chats", "session-x.jsonl") + "#1"
	st := map[string]string{}
	ls := lines(t, "chat.jsonl")
	for _, l := range ls[:3] {
		if evs, _ := (Adapter{}).ParseTranscriptLine(l, adapters.TranscriptMeta{ReceivedAt: t0, State: st, Warmup: true}); len(evs) != 0 {
			t.Fatal("warmup emitted events")
		}
	}
	evs, _ := Adapter{}.ParseTranscriptLine(ls[4], adapters.TranscriptMeta{ReceivedAt: t0, State: st, Ref: ref})
	if len(evs) == 0 || evs[0].SessionID != "gemini-cli:gs1a2b3c-0000-4000-8000-000000000001" || evs[0].Project == nil {
		t.Fatalf("after warmup: %+v", evs)
	}
}

func TestTranscriptRoots(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GEMINI_CLI_HOME", "")
	if r := (Adapter{}).TranscriptRoots(home, t0, t0); r != nil {
		t.Fatalf("roots without ~/.gemini/tmp: %v", r)
	}
	os.MkdirAll(filepath.Join(home, ".gemini", "tmp"), 0o700)
	if r := (Adapter{}).TranscriptRoots(home, t0, t0); len(r) != 2 {
		t.Fatalf("roots: %v", r)
	}
}

func TestRegistered(t *testing.T) {
	a, ok := adapters.Get(Name)
	if !ok {
		t.Fatal("not registered")
	}
	if _, ok := a.(adapters.TranscriptParser); !ok {
		t.Fatal("no transcript parser")
	}
	if _, ok := a.(adapters.TranscriptDiscoverer); !ok {
		t.Fatal("no transcript discovery")
	}
}

func TestNormalizeTool(t *testing.T) {
	for name, want := range map[string]string{
		"run_shell_command": model.ToolShell, "replace": model.ToolEdit, "write_file": model.ToolWrite,
		"read_many_files": model.ToolRead, "grep_search": model.ToolSearch, "google_web_search": model.ToolWeb,
		"invoke_agent": model.ToolTask, "mcp_tracker_list_issues": model.ToolMCP, "write_todos": model.ToolOther,
	} {
		if got := NormalizeTool(name); got != want {
			t.Errorf("%s: %s, want %s", name, got, want)
		}
	}
}

func TestJoinPath(t *testing.T) {
	cases := [][3]string{
		{"/home/dev/demo", "src/a.ts", "/home/dev/demo/src/a.ts"},
		{"/home/dev/demo", "/etc/x", "/etc/x"},
		{"/home/dev/demo", "", ""},
		{`C:\Users\dev\demo`, "src/a.ts", `C:\Users\dev\demo\src\a.ts`},
		{`C:\Users\dev\demo`, `D:\x.txt`, `D:\x.txt`},
	}
	for _, c := range cases {
		if got := joinPath(c[0], c[1]); got != c[2] {
			t.Errorf("joinPath(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}
