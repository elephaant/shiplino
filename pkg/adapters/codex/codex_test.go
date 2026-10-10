// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

package codex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/elephaant/shiplino/pkg/adapters"
	"github.com/elephaant/shiplino/pkg/model"
)

var update = flag.Bool("update", false, "rewrite golden files")

var t0 = time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)

func golden(t *testing.T, name string, events []model.Event) {
	t.Helper()
	for i := range events {
		events[i].ID = ""
		if err := events[i].Validate(); err == nil {
			continue
		}
	}
	b, _ := json.MarshalIndent(events, "", "  ")
	path := filepath.Join("testdata", "0.153", name)
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

func lines(t *testing.T, name string) [][]byte {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "0.153", name))
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

func TestHooksGolden(t *testing.T) {
	var all []model.Event
	for i, l := range lines(t, "hooks.jsonl") {
		evs, err := Adapter{}.ParseHook(l, adapters.HookMeta{EnvelopeID: fmt.Sprintf("env-%02d", i), ReceivedAt: t0.Add(time.Duration(i) * time.Second)})
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
	golden(t, "hooks.golden.json", all)

	byKind := map[model.Kind][]model.Event{}
	for _, e := range all {
		byKind[e.Kind] = append(byKind[e.Kind], e)
	}
	if sh := byKind[model.KindShellExec]; len(sh) != 1 || sh[0].Data["command"] != "rg -n health src" || sh[0].Data["exit_code"] != 1 {
		t.Errorf("shell: %+v", sh)
	}
	edits := byKind[model.KindFileEdit]
	if len(edits) != 2 || edits[0].Data["path"] != "/home/dev/demo/src/health.ts" || edits[0].Data["lines_added"] != 2 || edits[1].Data["lines_removed"] != 1 {
		t.Errorf("edits: %+v", edits)
	}
	if ts := byKind[model.KindTurnStart]; ts[0].DedupKey != "codex:th-1:turn:tu-1" {
		t.Errorf("turn key must match the rollout's: %s", ts[0].DedupKey)
	}
	if ends := byKind[model.KindTurnEnd]; len(ends) != 2 || ends[1].Data["status"] != "interrupted" {
		t.Errorf("turn ends: %+v", ends)
	}
}

func TestRolloutGolden(t *testing.T) {
	st := map[string]string{}
	var all []model.Event
	for i, l := range lines(t, "rollout.jsonl") {
		evs, err := Adapter{}.ParseTranscriptLine(l, adapters.TranscriptMeta{ReceivedAt: t0, State: st})
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
	golden(t, "rollout.golden.json", all)

	var usage []model.Event
	turns := 0
	for _, e := range all {
		if e.Kind == model.KindUsage {
			usage = append(usage, e)
		}
		if e.Kind == model.KindTurnStart {
			turns++
			if e.DedupKey == "codex:th-2:turn:" {
				t.Error("turn without id")
			}
		}
	}
	// token_count (a running total that resets) is NOT used; the two
	// per-response records are.
	if len(usage) != 3 || usage[2].Data["model"] != "gpt-5.6-terra-mini" || usage[0].Data["model"] != "gpt-5.6-terra" || usage[0].Data["input_tokens"] != int64(31527-12032) || usage[0].Data["cache_read_tokens"] != int64(12032) {
		t.Fatalf("usage: %+v", usage)
	}
	if turns != 3 || all[0].Agent.Version != "0.153.4" || all[0].Project.Branch != "main" {
		t.Fatalf("turns=%d first=%+v", turns, all[0])
	}
}

func TestWarmupRebuildsStateWithoutEvents(t *testing.T) {
	st := map[string]string{}
	ls := lines(t, "rollout.jsonl")
	for _, l := range ls[:4] {
		evs, _ := Adapter{}.ParseTranscriptLine(l, adapters.TranscriptMeta{ReceivedAt: t0, State: st, Warmup: true})
		if len(evs) != 0 {
			t.Fatal("warmup emitted events")
		}
	}
	if st["model"] != "gpt-5.6-terra" || st["session"] != "th-2" || st["turn"] != "tu-9" {
		t.Fatalf("state: %v", st)
	}
	evs, _ := Adapter{}.ParseTranscriptLine(ls[4], adapters.TranscriptMeta{ReceivedAt: t0, State: st})
	if len(evs) != 1 || evs[0].Data["model"] != "gpt-5.6-terra" {
		t.Fatalf("after warmup: %+v", evs)
	}
}

func TestHelperThreadsSkipped(t *testing.T) {
	st := map[string]string{}
	meta := `{"timestamp":"2026-10-09T10:00:00Z","type":"session_meta","payload":{"session_id":"g1","source":{"subagent":{"other":"guardian"}}}}`
	use := `{"timestamp":"2026-10-09T10:00:01Z","type":"token_usage_record","payload":{"response_id":"r","usage":{"input_tokens":5,"output_tokens":1}}}`
	for _, l := range []string{meta, use} {
		if evs, _ := (Adapter{}).ParseTranscriptLine([]byte(l), adapters.TranscriptMeta{ReceivedAt: t0, State: st}); len(evs) != 0 {
			t.Fatalf("helper thread produced events: %+v", evs)
		}
	}
}

func TestInstall(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".codex", "hooks.json")
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte(`{"description":"mine","hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"/usr/local/bin/policy"}]}]}}`), 0o600)
	bin := filepath.Join(dir, "with space", "shiplino")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	res, err := Install(path, bin, filepath.Join(dir, "backups"))
	if err != nil || !res.Changed || len(res.Events) != len(Events) {
		t.Fatalf("%+v %v", res, err)
	}
	b, _ := os.ReadFile(path)
	s := string(b)
	if !strings.Contains(s, "/usr/local/bin/policy") || !strings.Contains(s, `"description": "mine"`) {
		t.Fatalf("user content lost:\n%s", s)
	}
	if !strings.Contains(s, "hook --agent codex") || !strings.Contains(s, `"async": true`) {
		t.Fatalf("handler:\n%s", s)
	}
	if ok, cmd, _ := Installed(path); !ok || !strings.Contains(cmd, "with space") {
		t.Fatalf("Installed: %v %q", ok, cmd)
	}
	if res, _ := Install(path, bin, filepath.Join(dir, "backups")); res.Changed {
		t.Fatal("second install changed the file")
	}
	Uninstall(path, filepath.Join(dir, "backups"))
	if ok, _, _ := Installed(path); ok {
		t.Fatal("still installed")
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

func TestJoinPath(t *testing.T) {
	cases := [][3]string{
		{"/home/dev/demo", "src/a.ts", "/home/dev/demo/src/a.ts"},
		{"/home/dev/demo", "/etc/x", "/etc/x"},
		{`C:\Users\dev\demo`, "src/a.ts", `C:\Users\dev\demo\src\a.ts`},
		{`C:\Users\dev\demo`, `D:\x.txt`, `D:\x.txt`},
	}
	for _, c := range cases {
		if got := joinPath(c[0], c[1]); got != c[2] {
			t.Errorf("joinPath(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}

func TestPatchText(t *testing.T) {
	patch := "*** Begin Patch\n*** Update File: a.ts\n@@\n-x\n+y\n*** End Patch"
	for _, in := range []string{`{"command":` + jsonString(patch) + `}`, `{"input":` + jsonString(patch) + `}`, jsonString(patch)} {
		if got := patchText([]byte(in)); got != patch {
			t.Errorf("patchText(%s) = %q", in, got)
		}
	}
	files := patchFiles(patch, "")
	if len(files) != 1 || files[0]["patch"] != "@@\n-x\n+y\n" {
		t.Errorf("patchFiles: %v", files)
	}
}

func jsonString(s string) string { b, _ := json.Marshal(s); return string(b) }
