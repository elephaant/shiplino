// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

package opencode

import (
	"bufio"
	"bytes"
	"context"
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
	"github.com/elephaant/shiplino/pkg/engine"
	"github.com/elephaant/shiplino/pkg/model"
	opencodeplugin "github.com/elephaant/shiplino/plugins/opencode"
)

var update = flag.Bool("update", false, "rewrite golden files")

var t0 = time.Date(2026, 10, 10, 10, 0, 0, 0, time.UTC)

// hooks.jsonl is what the plugin sends for bus.jsonl (OpenCode bus events
// shaped like v1.18's); plugins/opencode/shiplino.test.mjs checks that.
const fixtures = "v1.18"

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

func TestHookMapping(t *testing.T) {
	all := parseFixture(t)
	by := map[model.Kind][]model.Event{}
	for _, e := range all {
		by[e.Kind] = append(by[e.Kind], e)
	}
	const root, child = "opencode:ses_root", "opencode:ses_root/sub:ses_child"
	if s := by[model.KindSessionStart]; len(s) != 1 || s[0].SessionID != root || s[0].Agent.Version != "1.18.35" || s[0].Project.CWD != "/home/dev/shop" {
		t.Errorf("session start: %+v", s)
	}
	if ts := by[model.KindTurnStart]; len(ts) != 2 || ts[0].Data["prompt"] != "fix the cart total rounding" || ts[1].ActorID != child || ts[1].ParentActor != root {
		t.Errorf("turn starts: %+v", ts)
	}
	if ss := by[model.KindSubagentStart]; len(ss) != 1 || ss[0].ActorID != root || ss[0].Data["child_session_id"] != child {
		t.Errorf("subagent start: %+v", ss)
	}
	if se := by[model.KindSubagentEnd]; len(se) != 1 || se[0].Data["child_session_id"] != child {
		t.Errorf("subagent end: %+v", se)
	}
	// Tool pairs share a call id and dedup key across the running and
	// finished updates.
	starts, ends := map[string]model.Event{}, map[string]model.Event{}
	for _, e := range by[model.KindToolStart] {
		if prev, ok := starts[e.DedupKey]; ok && !prev.TS.Equal(e.TS) {
			t.Errorf("start %s: times differ", e.DedupKey)
		}
		starts[e.DedupKey] = e
	}
	for _, e := range by[model.KindToolEnd] {
		ends[e.Data["tool_call_id"].(string)] = e
	}
	if len(starts) != 8 || len(ends) != 8 {
		t.Fatalf("tools: %d starts, %d ends", len(starts), len(ends))
	}
	if e := ends["toolu_bash1"]; e.Data["duration_ms"] != int64(2200) || e.Data["ok"] != true {
		t.Errorf("bash end: %+v", e.Data)
	}
	if e := ends["toolu_fetch1"]; e.Data["ok"] != false || e.Data["error"] != "network unreachable" {
		t.Errorf("failed tool: %+v", e.Data)
	}
	if e := ends["toolu_grep1"]; e.ActorID != child || e.SessionID != root {
		t.Errorf("subagent tool: %+v", e)
	}
	if sh := by[model.KindShellExec]; len(sh) != 1 || sh[0].Data["exit_code"] != 1 || sh[0].Data["command"] != "npm test" {
		t.Errorf("shell: %+v", sh)
	}
	if r := by[model.KindFileRead]; len(r) != 1 || r[0].Data["path"] != "/home/dev/shop/src/cart.ts" {
		t.Errorf("file read: %+v", r)
	}
	fe := by[model.KindFileEdit]
	if len(fe) != 4 {
		t.Fatalf("file edits: %+v", fe)
	}
	if d := fe[0].Data; d["lines_added"] != 1 || d["lines_source"] != "agent" || d["patch_source"] != "agent" || !strings.HasPrefix(d["patch"].(string), "@@ -11,3 +11,3 @@\n") {
		t.Errorf("edit: %+v", d)
	}
	if d := fe[1].Data; d["op"] != "create" || d["lines_added"] != 3 || d["lines_source"] != "computed" {
		t.Errorf("write: %+v", d)
	}
	if d := fe[3].Data; d["op"] != "delete" || d["lines_removed"] != 4 || d["path"] != "/home/dev/shop/src/old.ts" {
		t.Errorf("apply_patch delete: %+v", d)
	}
	u := by[model.KindUsage]
	if len(u) != 4 {
		t.Fatalf("usage: %+v", u)
	}
	if d := u[0].Data; u[0].ActorID != child || d["cost_usd"] != 0.0123 || d["cost_source"] != "reported" || d["cache_read_tokens"] != int64(7000) {
		t.Errorf("subagent usage: %+v", u[0])
	}
	if d := u[1].Data; d["output_tokens"] != int64(1000) || d["reasoning_tokens"] != int64(200) || !u[1].TS.Equal(time.UnixMilli(1791626414000)) {
		t.Errorf("usage (reasoning billed as output, completion time): %+v", u[1])
	}
	if d := u[2].Data; d["cost_source"] != "computed" {
		t.Errorf("zero cost falls back to the price table: %+v", d)
	}
	if d := u[3].Data; d["cost_source"] != "unpriced" {
		t.Errorf("unknown model: %+v", d)
	}
	w := by[model.KindWaitingStart]
	if len(w) != 3 || w[0].Data["message"] != "edit src/cart.ts" || w[1].Data["reason"] != "question" || w[2].Data["message"] != "git push" {
		t.Errorf("waiting: %+v", w)
	}
	if we := by[model.KindWaitingEnd]; len(we) != 3 || we[2].Data["resolution"] != "reject" {
		t.Errorf("waiting end: %+v", we)
	}
	if e := by[model.KindError]; len(e) != 1 || e[0].Data["message"] != "Overloaded" {
		t.Errorf("errors (aborts skipped): %+v", e)
	}
	if e := by[model.KindSessionEnd]; len(e) != 1 || e[0].SessionID != "opencode:ses_prev" {
		t.Errorf("session end: %+v", e)
	}
}

// The whole fixture folded by the engine: one root with a nested
// subagent, OpenCode's own cost shown as reported.
func TestEngine(t *testing.T) {
	e := engine.New(nil, nil)
	seen := map[string]bool{} // the store applies each dedup key once
	for _, ev := range parseFixture(t) {
		if !seen[ev.DedupKey] {
			seen[ev.DedupKey] = true
			e.Apply(ev)
		}
	}
	if r := e.Get("opencode:ses_root"); r.ToolCalls != 7 {
		t.Errorf("root tool calls: %d", r.ToolCalls)
	}
	r, c := e.Get("opencode:ses_root"), e.Get("opencode:ses_root/sub:ses_child")
	if r == nil || c == nil {
		t.Fatal("missing sessions")
	}
	if r.Title != "Fix cart total rounding" || c.Title != "Check tests (@explore subagent)" || c.ParentID != r.ID || c.ActorType != "explore" {
		t.Errorf("titles/tree: root %q, child %q parent %q type %q", r.Title, c.Title, c.ParentID, c.ActorType)
	}
	if c.Status != engine.StatusDone || r.Status != engine.StatusReview {
		t.Errorf("status: root %s, child %s", r.Status, c.Status)
	}
	if c.CostSource != "reported" || c.BestCostUSD != 0.0123 {
		t.Errorf("child cost: %v %s", c.BestCostUSD, c.CostSource)
	}
	if r.CostSource != "computed" || r.TreeAgentCostUSD != 0.0123+0.0456 {
		t.Errorf("root cost (one response priced by Shiplino): %v %s", r.TreeAgentCostUSD, r.CostSource)
	}
}

func TestParseErrors(t *testing.T) {
	if _, err := (Adapter{}).ParseHook([]byte(`{"session_id":"s","hook_event_name":"tui.toast.show"}`), adapters.HookMeta{}); !errors.Is(err, adapters.ErrUnknownEvent) {
		t.Errorf("unknown event: %v", err)
	}
	if _, err := (Adapter{}).ParseHook([]byte(`{"hook_event_name":"session.idle"}`), adapters.HookMeta{}); err == nil {
		t.Error("missing session id accepted")
	}
	if _, err := (Adapter{}).ParseHook([]byte(`nope`), adapters.HookMeta{}); err == nil {
		t.Error("garbage accepted")
	}
}

func TestDetect(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("PATH", t.TempDir())
	if d := Detect(context.Background(), home); d.Installed || d.PluginPath != filepath.Join(home, ".config", "opencode", "plugins", "shiplino.js") {
		t.Errorf("empty home: %+v", d)
	}
	os.MkdirAll(filepath.Join(home, ".local", "share", "opencode"), 0o700)
	if d := Detect(context.Background(), home); !d.Installed {
		t.Errorf("data dir: %+v", d)
	}
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	if d := Detect(context.Background(), home); d.PluginPath != filepath.Join(xdg, "opencode", "plugins", "shiplino.js") {
		t.Errorf("XDG_CONFIG_HOME: %+v", d)
	}
}

func TestInstall(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin", "shiplino")
	if !filepath.IsAbs(bin) {
		t.Fatal(bin)
	}
	dir := filepath.Join(t.TempDir(), "opencode", "plugins")
	path := filepath.Join(dir, PluginFile)
	backups := t.TempDir()

	if _, err := Install(path, "shiplino", backups); err == nil {
		t.Error("relative binary path accepted")
	}
	// The user's own plugins stay as they are.
	other := filepath.Join(dir, "mine.ts")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(other, []byte("export const Mine = async () => ({})\n"), 0o644)

	res, err := Install(path, bin, backups)
	if err != nil || !res.Changed || res.Backup != "" || len(res.Events) == 0 {
		t.Fatalf("fresh install: %+v %v", res, err)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, Render(bin)) || bytes.Contains(got, []byte(opencodeplugin.BinPlaceholder)) {
		t.Fatal("installed file isn't the rendered plugin")
	}
	ok, cmd, err := Installed(path)
	if !ok || err != nil || cmd != bin+" hook --agent opencode" {
		t.Errorf("Installed: %v %q %v", ok, cmd, err)
	}

	// Idempotent.
	if res, err := Install(path, bin, backups); err != nil || res.Changed {
		t.Errorf("second install: %+v %v", res, err)
	}
	// A new binary path replaces our file, after a backup.
	bin2 := filepath.Join(filepath.Dir(bin), "shiplino2")
	if res, err := Install(path, bin2, backups); err != nil || !res.Changed || res.Backup == "" {
		t.Errorf("update: %+v %v", res, err)
	}
	if _, cmd, _ := Installed(path); !strings.HasPrefix(cmd, bin2+" ") {
		t.Errorf("updated command: %q", cmd)
	}

	// Uninstall removes only our file.
	if res, err := Uninstall(path, backups); err != nil || !res.Changed || res.Backup == "" {
		t.Errorf("uninstall: %+v %v", res, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Error("plugin still there")
	}
	if b, _ := os.ReadFile(other); string(b) != "export const Mine = async () => ({})\n" {
		t.Error("user plugin changed")
	}
	if res, err := Uninstall(path, backups); err != nil || res.Changed {
		t.Errorf("uninstall twice: %+v %v", res, err)
	}
	if ok, _, err := Installed(path); ok || err != nil {
		t.Errorf("Installed after uninstall: %v %v", ok, err)
	}
}

// A file named shiplino.js that Shiplino didn't write is never changed.
func TestForeignFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), PluginFile)
	mine := []byte("// my own plugin\nexport const X = async () => ({})\n")
	os.WriteFile(path, mine, 0o644)
	bin := filepath.Join(t.TempDir(), "shiplino")
	if _, err := Install(path, bin, t.TempDir()); !errors.Is(err, ErrNotOurs) {
		t.Errorf("install over a foreign file: %v", err)
	}
	if res, err := Uninstall(path, t.TempDir()); err != nil || res.Changed {
		t.Errorf("uninstall of a foreign file: %+v %v", res, err)
	}
	if ok, _, err := Installed(path); ok || !errors.Is(err, ErrNotOurs) {
		t.Errorf("Installed: %v %v", ok, err)
	}
	if b, _ := os.ReadFile(path); !bytes.Equal(b, mine) {
		t.Error("foreign file changed")
	}
}

// Paths with quotes, backslashes and non-ASCII characters stay valid JS.
func TestRenderQuoting(t *testing.T) {
	for _, bin := range []string{`C:\Users\dev\.shiplino\bin\shiplino.exe`, `/home/dev/we"ird/shiplino`, "/home/dév/\u2028/shiplino"} {
		b := Render(bin)
		m := binRe.FindSubmatch(b)
		var s string
		if m == nil || json.Unmarshal(m[1], &s) != nil || s != bin {
			t.Errorf("%q rendered as %q", bin, m)
		}
		if bytes.Contains(b, []byte("\u2028")) {
			t.Errorf("%q: raw line separator in the source", bin)
		}
	}
}
