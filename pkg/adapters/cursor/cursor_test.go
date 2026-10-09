// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package cursor

import (
	"bufio"
	"bytes"
	"encoding/json"
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

var t0 = time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)

func parseFixture(t *testing.T) []model.Event {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "2026.09", "hooks.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var all []model.Event
	sc := bufio.NewScanner(f)
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
	path := filepath.Join("testdata", "2026.09", "hooks.golden.json")
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

func TestHookMapping(t *testing.T) {
	by := map[model.Kind][]model.Event{}
	for _, e := range parseFixture(t) {
		by[e.Kind] = append(by[e.Kind], e)
	}
	if ts := by[model.KindTurnStart]; len(ts) != 1 || ts[0].DedupKey != "cursor:cv-1:turn:g-1" || ts[0].Data["prompt"] != "fix the cart total rounding" {
		t.Errorf("turn start: %+v", ts)
	}
	if te := by[model.KindTurnEnd]; len(te) != 2 || te[1].Data["status"] != "interrupted" {
		t.Errorf("turn ends: %+v", te)
	}
	starts := by[model.KindToolStart]
	if len(starts) != 5 || !starts[0].TS.Equal(t0.Add(2*time.Second-120*time.Millisecond)) {
		t.Errorf("tool starts (placed duration before the end): %+v", starts)
	}
	sh := by[model.KindShellExec]
	if len(sh) != 2 || sh[0].Data["exit_code"] != 1 || sh[1].Data["exit_code"] != 0 || sh[1].Data["command"] != "npm test" {
		t.Errorf("shell: %+v", sh)
	}
	fe := by[model.KindFileEdit]
	if len(fe) != 1 || fe[0].Data["path"] != "/home/dev/shop/src/cart.ts" || fe[0].Data["lines_added"] != 2 || fe[0].Data["lines_removed"] != 1 {
		t.Errorf("file edit: %+v", fe)
	}
	// The subagent's own events attach to the parent session.
	var sub model.Event
	for _, e := range starts {
		if e.Data["tool_call_id"] == "tu-s1" {
			sub = e
		}
	}
	if sub.SessionID != "cursor:cv-1" || sub.ActorID != "cursor:cv-1/sub:sa-9" || sub.ParentActor != "cursor:cv-1" {
		t.Errorf("subagent event: %+v", sub)
	}
	if se := by[model.KindSubagentEnd]; len(se) != 1 || se[0].Data["child_session_id"] != "cursor:cv-1/sub:sa-9" {
		t.Errorf("subagent end: %+v", se)
	}
	u := by[model.KindUsage]
	if len(u) != 1 || u[0].Data["input_tokens"] != int64(1200) || u[0].Data["cache_read_tokens"] != int64(50000) || u[0].Data["cost_source"] != "computed" {
		t.Errorf("usage: %+v", u)
	}
	if m := by[model.KindMCPCall]; len(m) != 1 || m[0].Data["server"] != "linear" {
		t.Errorf("mcp: %+v", m)
	}
	for _, e := range parseFixture(t) {
		if b, _ := json.Marshal(e); strings.Contains(string(b), "dev@example.com") {
			t.Fatal("user email stored")
		}
	}
}

func TestLineDiff(t *testing.T) {
	cases := []struct {
		old, new string
		a, r     int
	}{
		{"", "a\nb\n", 2, 0},
		{"a\nb\n", "", 0, 2},
		{"a\nb\nc", "a\nX\nc", 1, 1},
		{"same", "same", 0, 0},
	}
	for _, c := range cases {
		if a, r := lineDiff(c.old, c.new); a != c.a || r != c.r {
			t.Errorf("lineDiff(%q,%q) = +%d -%d, want +%d -%d", c.old, c.new, a, r, c.a, c.r)
		}
	}
}

func TestInstall(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".cursor", "hooks.json")
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte(`{"version":1,"hooks":{"afterFileEdit":[{"command":"./format.sh"}],"beforeShellExecution":[{"command":"/usr/bin/guard","failClosed":true}]}}`), 0o600)
	bin := filepath.Join(dir, "bin", "shiplino")
	res, err := Install(path, bin, filepath.Join(dir, "backups"))
	if err != nil || !res.Changed {
		t.Fatalf("%+v %v", res, err)
	}
	b, _ := os.ReadFile(path)
	s := string(b)
	for _, want := range []string{"./format.sh", "/usr/bin/guard", "hook --agent cursor"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q:\n%s", want, s)
		}
	}
	var doc struct {
		Version int                         `json:"version"`
		Hooks   map[string][]map[string]any `json:"hooks"`
	}
	json.Unmarshal(b, &doc)
	if doc.Version != 1 || len(doc.Hooks["afterFileEdit"]) != 2 {
		t.Fatalf("doc: %+v", doc)
	}
	for ev, hs := range doc.Hooks {
		for _, h := range hs {
			cmd, _ := h["command"].(string)
			if strings.Contains(cmd, "shiplino") && (h["failClosed"] != nil || h["type"] != nil || strings.HasPrefix(ev, "before") && ev != "beforeSubmitPrompt" || ev == "preToolUse" || ev == "subagentStart") {
				t.Fatalf("unsafe registration %s: %v", ev, h)
			}
		}
	}
	if res, _ := Install(path, bin, filepath.Join(dir, "backups")); res.Changed {
		t.Fatal("second install changed the file")
	}
	if ok, cmd, _ := Installed(path); !ok || !strings.Contains(cmd, bin) {
		t.Fatalf("Installed: %v %q", ok, cmd)
	}
	Uninstall(path, filepath.Join(dir, "backups"))
	b, _ = os.ReadFile(path)
	if strings.Contains(string(b), "shiplino") || !strings.Contains(string(b), "./format.sh") {
		t.Fatalf("after uninstall:\n%s", b)
	}
}

func TestInstallNewFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	if _, err := Install(path, "/opt/shiplino/bin/shiplino", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.HasPrefix(strings.TrimSpace(string(b)), "{\n  \"version\": 1,") {
		t.Fatalf("version must come first:\n%s", b)
	}
}
