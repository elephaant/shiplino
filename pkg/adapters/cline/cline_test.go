// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

package cline

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/elephaant/shiplino/pkg/adapters"
	"github.com/elephaant/shiplino/pkg/model"
)

var update = flag.Bool("update", false, "rewrite golden files")

var t0 = time.Date(2026, 10, 10, 8, 0, 0, 0, time.UTC)

const fixtures = "source-2026-10-10"

func parseFixture(t *testing.T) []model.Event {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", fixtures, "hooks.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var all []model.Event
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for i := 0; sc.Scan(); i++ {
		evs, err := Adapter{}.ParseHook(sc.Bytes(), adapters.HookMeta{EnvelopeID: fmt.Sprintf("env-%02d", i), ReceivedAt: t0.Add(time.Duration(i) * time.Second)})
		if err != nil {
			t.Fatalf("line %d: %v", i+1, err)
		}
		for _, e := range evs {
			if err := e.Validate(); err != nil {
				t.Fatalf("line %d: %v", i+1, err)
			}
		}
		all = append(all, evs...)
	}
	return all
}

func TestHooksGolden(t *testing.T) {
	all := parseFixture(t)
	for i := range all {
		all[i].ID = ""
	}
	b, _ := json.MarshalIndent(all, "", "  ")
	path := filepath.Join("testdata", fixtures, "hooks.golden.json")
	if *update {
		os.WriteFile(path, append(b, '\n'), 0o644)
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update)", err)
	}
	if !bytes.Equal(bytes.TrimSpace(want), bytes.TrimSpace(b)) {
		t.Errorf("events differ from %s; run with -update and review the diff", path)
	}
}

// byKind groups a session's events.
func byKind(t *testing.T, sid string) map[model.Kind][]model.Event {
	t.Helper()
	by := map[model.Kind][]model.Event{}
	for _, e := range parseFixture(t) {
		if e.SessionID == sid {
			by[e.Kind] = append(by[e.Kind], e)
		}
	}
	return by
}

// The VS Code extension's dialect: TaskStart-style names, millisecond
// timestamps, no tool call ids.
func TestExtensionMapping(t *testing.T) {
	by := byKind(t, "cline:1791619200000")
	ss := by[model.KindSessionStart]
	if len(ss) != 2 || ss[0].DedupKey != ss[1].DedupKey || !ss[0].TS.Equal(time.UnixMilli(1791619200000)) {
		t.Fatalf("session start (once per task, Cline's time): %+v", ss)
	}
	if ss[0].Agent.Version != "3.40.0" || ss[0].Project.CWD != "/home/dev/shop" || ss[0].Data["model"] != nil {
		t.Errorf("version, cwd, \"unknown\" model: %+v", ss[0])
	}
	if ts := by[model.KindTurnStart]; len(ts) != 1 || ts[0].Data["prompt"] != "fix the cart total rounding" {
		t.Errorf("turn start: %+v", ts)
	}
	te := by[model.KindTurnEnd]
	if len(te) != 2 || te[0].Data["status"] != "ok" || te[1].Data["status"] != "interrupted" {
		t.Errorf("turn ends: %+v", te)
	}
	starts, ends := by[model.KindToolStart], by[model.KindToolEnd]
	if len(starts) != 4 || len(ends) != 4 {
		t.Fatalf("tools: %d starts, %d ends", len(starts), len(ends))
	}
	for i, want := range []string{model.ToolRead, model.ToolEdit, model.ToolShell, model.ToolMCP} {
		s, e := starts[i], ends[i]
		if s.Data["tool"] != want || s.Data["tool_call_id"] != e.Data["tool_call_id"] || !strings.HasPrefix(s.Data["tool_call_id"].(string), "cl-env-") {
			t.Errorf("tool %d: %+v / %+v", i, s.Data, e.Data)
		}
		// The start is the end minus Cline's execution time.
		if d := e.Data["duration_ms"].(int64); e.TS.Sub(s.TS) != time.Duration(d)*time.Millisecond {
			t.Errorf("tool %d timing: %v → %v, %d ms", i, s.TS, e.TS, d)
		}
	}
	if fr := by[model.KindFileRead]; len(fr) != 1 || fr[0].Data["path"] != "/home/dev/shop/src/cart.ts" {
		t.Errorf("file read: %+v", fr)
	}
	fe := by[model.KindFileEdit]
	if len(fe) != 1 || fe[0].Data["path"] != "/home/dev/shop/src/cart.ts" || fe[0].Data["lines_added"] != 2 || fe[0].Data["lines_removed"] != 1 || fe[0].Data["patch_source"] != "computed" {
		t.Errorf("file edit (relative path resolved): %+v", fe)
	}
	sh := by[model.KindShellExec]
	if len(sh) != 1 || sh[0].Data["command"] != "npm test" || sh[0].Data["exit_code"] != nil || sh[0].Data["duration_ms"] != int64(3200) {
		t.Errorf("shell (no exit code in this dialect): %+v", sh)
	}
	if m := by[model.KindMCPCall]; len(m) != 1 || m[0].Data["server"] != "github" || m[0].Data["tool"] != "create_issue" {
		t.Errorf("mcp: %+v", m)
	}
}

// The CLI/SDK dialect: snake_case names, RFC 3339 times, call ids, root
// session ids and subagents.
func TestSDKMapping(t *testing.T) {
	by := byKind(t, "cline:sess-01")
	ss := by[model.KindSessionStart]
	if len(ss) != 2 || ss[0].Project.Branch != "fix/timeouts" || ss[1].Data["source"] != "resume" {
		t.Fatalf("session starts: %+v", ss)
	}
	if ts := by[model.KindTurnStart]; len(ts) != 1 || ts[0].Data["prompt"] != "raise the client timeout" {
		t.Errorf("turn start: %+v", ts)
	}
	te := by[model.KindTurnEnd]
	if len(te) != 2 || te[0].Data["status"] != "error" || te[0].Data["error"] != "rate limited (429)" || te[1].Data["status"] != "ok" {
		t.Errorf("turn ends: %+v", te)
	}
	starts := by[model.KindToolStart]
	if len(starts) != 4 || starts[0].DedupKey != "cline:sess-01:call_1:start" || !starts[0].TS.Equal(time.Date(2026, 10, 10, 9, 0, 4, 950e6, time.UTC)) {
		t.Fatalf("tool starts (Cline's own startedAt): %+v", starts)
	}
	fe := by[model.KindFileEdit]
	if len(fe) != 3 {
		t.Fatalf("file edits: %+v", fe)
	}
	if d := fe[0].Data; d["path"] != "/home/dev/api/client/http.go" || d["op"] != "modify" || d["lines_added"] != 1 || d["lines_removed"] != 1 || d["patch_source"] != "agent" {
		t.Errorf("patch update: %+v", d)
	}
	if d := fe[1].Data; d["path"] != "/home/dev/api/client/doc.go" || d["op"] != "create" || d["lines_added"] != 2 {
		t.Errorf("patch add: %+v", d)
	}
	if d := fe[2].Data; d["path"] != "/home/dev/api/client/http_test.go" || d["op"] != "create" || d["lines_added"] != 3 {
		t.Errorf("editor create: %+v", d)
	}
	sh := by[model.KindShellExec]
	if len(sh) != 2 || sh[0].Data["command"] != "go test ./..." || sh[0].Data["exit_code"] != 1 || sh[1].Data["exit_code"] != 0 || sh[0].Data["duration_ms"] != nil {
		t.Errorf("shell (exit codes from Cline's output): %+v", sh)
	}
	fr := by[model.KindFileRead]
	if len(fr) != 2 || fr[1].Data["path"] != "/home/dev/api/client/http.go" {
		t.Fatalf("subagent reads: %+v", fr)
	}
	if e := fr[0]; e.ActorID != "cline:sess-01/sub:agent-sub-1" || e.ParentActor != "cline:sess-01" || e.ActorType != "subagent" {
		t.Errorf("subagent actor: %+v", e)
	}
	if se := by[model.KindSessionEnd]; len(se) != 1 || se[0].Data["reason"] != "session_disposed" {
		t.Errorf("session end: %+v", se)
	}
	// PreToolUse isn't registered; a stray payload records nothing.
	for _, e := range starts {
		if e.Data["tool"] == model.ToolWeb {
			t.Errorf("tool_call recorded: %+v", e)
		}
	}
}

// Tool output and assistant text are never stored.
func TestNoContentStored(t *testing.T) {
	for _, e := range parseFixture(t) {
		b, _ := json.Marshal(e)
		for _, s := range []string{"FAIL client", "Created issue", "Raised the timeout", "Totals now round", "package client\"", "[object Object]"} {
			if strings.Contains(string(b), s) {
				t.Fatalf("content %q stored: %s", s, b)
			}
		}
	}
}

func TestParseErrors(t *testing.T) {
	if _, err := (Adapter{}).ParseHook([]byte(`{"hookName":"TaskStart"}`), adapters.HookMeta{}); err == nil {
		t.Error("missing taskId accepted")
	}
	if _, err := (Adapter{}).ParseHook([]byte(`[`), adapters.HookMeta{}); err == nil {
		t.Error("bad JSON accepted")
	}
	_, err := Adapter{}.ParseHook([]byte(`{"hookName":"Notification","taskId":"t"}`), adapters.HookMeta{})
	if !errors.Is(err, adapters.ErrUnknownEvent) {
		t.Errorf("unknown event: %v", err)
	}
	for _, ev := range []string{"PreToolUse", "PreCompact", "pre_compact"} {
		if evs, err := (Adapter{}).ParseHook([]byte(`{"hookName":"`+ev+`","taskId":"t"}`), adapters.HookMeta{}); err != nil || evs != nil {
			t.Errorf("%s: %v %v", ev, evs, err)
		}
	}
	// A minimal-level payload (content stripped by the shim) still works.
	evs, err := Adapter{}.ParseHook([]byte(`{"hookName":"tool_result","taskId":"t","tool_result":{"id":"c","name":"run_commands","durationMs":5}}`), adapters.HookMeta{ReceivedAt: t0})
	if err != nil || len(evs) != 2 {
		t.Errorf("stripped tool call: %+v %v", evs, err)
	}
	// The hook time is the fallback when the payload's time doesn't parse.
	evs, err = Adapter{}.ParseHook([]byte(`{"hookName":"TaskComplete","taskId":"t","timestamp":"soon"}`), adapters.HookMeta{ReceivedAt: t0})
	if err != nil || len(evs) != 1 || !evs[0].TS.Equal(t0) {
		t.Errorf("time fallback: %+v %v", evs, err)
	}
}

func TestNormalizeTool(t *testing.T) {
	for name, want := range map[string]string{
		"read_files": model.ToolRead, "editor": model.ToolEdit, "apply_patch": model.ToolEdit,
		"run_commands": model.ToolShell, "search_codebase": model.ToolSearch, "fetch_web_content": model.ToolWeb,
		"spawn_agent": model.ToolTask, "team_spawn_teammate": model.ToolTask, "linear__get_issue": model.ToolMCP,
		"ask_question": model.ToolOther, "skills": model.ToolOther, "submit_and_exit": model.ToolOther,
	} {
		if got := NormalizeTool(name); got != want {
			t.Errorf("%s = %s, want %s", name, got, want)
		}
	}
}

func TestInputShapes(t *testing.T) {
	read := map[string]json.RawMessage{"paths": json.RawMessage(`["/a","/b"]`), "file_paths": json.RawMessage(`"/c"`)}
	if got := strings.Join(readPaths(read), ","); got != "/a,/b,/c" {
		t.Errorf("read paths: %s", got)
	}
	cmds := map[string]json.RawMessage{"commands": json.RawMessage(`[{"command":"git","args":["log","-1"]},"ls"]`)}
	if got := strings.Join(commands(cmds), ";"); got != "git log -1;ls" {
		t.Errorf("commands: %s", got)
	}
	b := builder{cwd: `C:\dev\shop`, p: &payload{}}
	if got := b.abs("src/cart.ts"); got != `C:\dev\shop\src\cart.ts` {
		t.Errorf("windows path: %s", got)
	}
}
