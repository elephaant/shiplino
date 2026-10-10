// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package windsurf

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
	"github.com/elephaant/shiplino/pkg/adapters/internal/hookfile"
	"github.com/elephaant/shiplino/pkg/model"
)

var update = flag.Bool("update", false, "rewrite golden files")

var t0 = time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)

const fixtures = "docs-2026-10-10"

func parseFixture(t *testing.T) []model.Event {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", fixtures, "hooks.jsonl"))
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
	by := map[model.Kind][]model.Event{}
	for _, e := range parseFixture(t) {
		if e.SessionID != "windsurf:tr-1" || e.ActorID != e.SessionID {
			t.Fatalf("session: %+v", e)
		}
		by[e.Kind] = append(by[e.Kind], e)
	}
	ts := by[model.KindTurnStart]
	if len(ts) != 2 || ts[0].DedupKey != "windsurf:tr-1:turn:ex-1" || ts[0].TurnID != "ex-1" || ts[0].Data["prompt"] != "fix the cart total rounding" {
		t.Errorf("turn start: %+v", ts)
	}
	// Windsurf's timestamp wins; the hook time is the fallback.
	if !ts[0].TS.Equal(t0.Add(250*time.Millisecond)) || !ts[0].ReceivedAt.Equal(t0) {
		t.Errorf("turn start time: %v (received %v)", ts[0].TS, ts[0].ReceivedAt)
	}
	if su := by[model.KindSessionUpdate]; len(su) != 1 || su[0].Data["model"] != "Claude Opus 5.5" {
		t.Errorf("model (\"Unknown\" skipped): %+v", su)
	}
	te := by[model.KindTurnEnd]
	if len(te) != 2 || te[0].DedupKey != "windsurf:tr-1:turnend:ex-1" || !te[1].TS.Equal(t0.Add(7*time.Second)) {
		t.Errorf("turn ends: %+v", te)
	}
	starts, ends := by[model.KindToolStart], by[model.KindToolEnd]
	if len(starts) != 4 || len(ends) != 4 {
		t.Fatalf("tools: %d starts, %d ends", len(starts), len(ends))
	}
	for i, want := range []string{model.ToolRead, model.ToolEdit, model.ToolShell, model.ToolMCP} {
		if starts[i].Data["tool"] != want || starts[i].Data["tool_call_id"] != ends[i].Data["tool_call_id"] {
			t.Errorf("tool %d: %+v / %+v", i, starts[i].Data, ends[i].Data)
		}
	}
	if fr := by[model.KindFileRead]; len(fr) != 1 || fr[0].Data["path"] != "/home/dev/shop/src/cart.ts" || fr[0].Project.CWD != "/home/dev/shop/src" {
		t.Errorf("file read: %+v", fr)
	}
	fe := by[model.KindFileEdit]
	if len(fe) != 1 || fe[0].Data["lines_added"] != 3 || fe[0].Data["lines_removed"] != 1 || fe[0].Data["lines_source"] != "computed" {
		t.Errorf("file edit: %+v", fe)
	}
	sh := by[model.KindShellExec]
	if len(sh) != 1 || sh[0].Data["command"] != "npm test" || sh[0].Project.CWD != "/home/dev/shop" || sh[0].Data["exit_code"] != nil {
		t.Errorf("shell: %+v", sh)
	}
	if m := by[model.KindMCPCall]; len(m) != 1 || m[0].Data["server"] != "github" || m[0].Data["tool"] != "create_issue" {
		t.Errorf("mcp: %+v", m)
	}
	for _, e := range parseFixture(t) {
		if b, _ := json.Marshal(e); strings.Contains(string(b), "Rounding") || strings.Contains(string(b), "Planner") {
			t.Fatalf("tool output or response stored: %s", b)
		}
	}
}

func TestParseErrors(t *testing.T) {
	if _, err := (Adapter{}).ParseHook([]byte(`{"agent_action_name":"post_read_code"}`), adapters.HookMeta{}); err == nil {
		t.Error("missing trajectory_id accepted")
	}
	_, err := Adapter{}.ParseHook([]byte(`{"agent_action_name":"post_setup_worktree","trajectory_id":"t"}`), adapters.HookMeta{})
	if !errors.Is(err, adapters.ErrUnknownEvent) {
		t.Errorf("unknown event: %v", err)
	}
	// The --event flag names the event when the payload doesn't.
	evs, err := Adapter{}.ParseHook([]byte(`{"trajectory_id":"t","tool_info":{}}`), adapters.HookMeta{Event: "post_cascade_response", ReceivedAt: t0})
	if err != nil || len(evs) != 1 || evs[0].Kind != model.KindTurnEnd {
		t.Errorf("event from meta: %+v %v", evs, err)
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

func TestDetect(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	if d := Detect(context.Background(), home); d.Installed {
		t.Fatalf("found without a config dir: %+v", d)
	}
	os.MkdirAll(filepath.Join(home, ".codeium", "windsurf"), 0o700)
	d := Detect(context.Background(), home)
	if !d.Installed || d.HooksPath != filepath.Join(home, ".codeium", "windsurf", "hooks.json") {
		t.Fatalf("%+v", d)
	}
}

// The transcript hook makes Windsurf write whole conversations to disk.
func TestNoTranscriptHook(t *testing.T) {
	for _, ev := range Events {
		if strings.Contains(ev, "transcript") {
			t.Fatalf("registers %s", ev)
		}
	}
}

func TestDetectJetBrains(t *testing.T) {
	cases := []struct {
		name  string
		setup func(home string)
		want  bool
	}{
		{"nothing", func(string) {}, false},
		{"only the Windsurf editor", func(h string) { os.MkdirAll(filepath.Join(h, ".codeium", "windsurf"), 0o700) }, false},
		{"another plugin", func(h string) {
			os.MkdirAll(filepath.Join(h, ".local", "share", "JetBrains", "IntelliJIdea2026.2", "python"), 0o700)
		}, false},
		{"hooks file", func(h string) {
			os.MkdirAll(filepath.Join(h, ".codeium"), 0o700)
			os.WriteFile(filepath.Join(h, ".codeium", "hooks.json"), []byte("{}"), 0o600)
		}, true},
		{"linux plugin", func(h string) {
			os.MkdirAll(filepath.Join(h, ".local", "share", "JetBrains", "PyCharm2026.2", "codeium"), 0o700)
		}, true},
		{"macos plugin", func(h string) {
			os.MkdirAll(filepath.Join(h, "Library", "Application Support", "JetBrains", "GoLand2026.2", "plugins", "Windsurf"), 0o700)
		}, true},
		{"windows plugin", func(h string) {
			os.MkdirAll(filepath.Join(h, "AppData", "Roaming", "JetBrains", "WebStorm2026.2", "plugins", "codeium-intellij"), 0o700)
		}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			c.setup(home)
			d := DetectJetBrains(home)
			if d.Installed != c.want || d.HooksPath != filepath.Join(home, ".codeium", "hooks.json") {
				t.Fatalf("%+v, want installed=%v", d, c.want)
			}
		})
	}
}

// ours returns Shiplino's handlers by event.
func ours(t *testing.T, b []byte) map[string][]map[string]any {
	t.Helper()
	var doc struct {
		Hooks map[string][]map[string]any `json:"hooks"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	out := map[string][]map[string]any{}
	for ev, hs := range doc.Hooks {
		for _, h := range hs {
			if cmd, _ := h["command"].(string); strings.Contains(cmd, "--agent windsurf") {
				out[ev] = append(out[ev], h)
			}
		}
	}
	return out
}

func TestInstall(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".codeium", "windsurf", "hooks.json")
	os.MkdirAll(filepath.Dir(path), 0o700)
	orig := []byte(`{"hooks":{"post_write_code":[{"command":"bash /home/dev/hooks/fmt.sh","show_output":true}],"pre_run_command":[{"command":"python3 /home/dev/hooks/guard.py"}]},"other":1}`)
	os.WriteFile(path, orig, 0o600)
	bin := filepath.Join(dir, "bin", "shiplino")
	backups := filepath.Join(dir, "backups")
	res, err := Install(path, bin, backups)
	if err != nil || !res.Changed || res.Backup == "" || len(res.Events) != len(Events) {
		t.Fatalf("%+v %v", res, err)
	}
	b, _ := os.ReadFile(path)
	for _, want := range []string{"fmt.sh", "guard.py", `"other": 1`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("missing %q:\n%s", want, b)
		}
	}
	got := ours(t, b)
	if len(got) != len(Events) {
		t.Fatalf("registered for %d events, want %d:\n%s", len(got), len(Events), b)
	}
	for ev, hs := range got {
		// Never a hook that exists to block, and never visible output.
		if strings.HasPrefix(ev, "pre_") && ev != "pre_user_prompt" || len(hs) != 1 || hs[0]["show_output"] != false {
			t.Fatalf("unsafe registration %s: %v", ev, hs)
		}
	}
	if res, _ := Install(path, bin, backups); res.Changed {
		t.Fatal("second install changed the file")
	}
	if ok, cmd, _ := Installed(path); !ok || !strings.Contains(cmd, bin) {
		t.Fatalf("Installed: %v %q", ok, cmd)
	}
	// A new binary path updates our entries in place.
	bin2 := filepath.Join(dir, "bin2", "shiplino")
	if _, err := Install(path, bin2, backups); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	if _, cmd, _ := Installed(path); !strings.Contains(cmd, bin2) || len(ours(t, b)["post_write_code"]) != 1 {
		t.Fatalf("not updated in place:\n%s", b)
	}
	if res, err := Uninstall(path, backups); err != nil || !res.Changed {
		t.Fatalf("uninstall: %+v %v", res, err)
	}
	b, _ = os.ReadFile(path)
	if !hookfile.SameJSON(orig, b) {
		t.Fatalf("user hooks changed after uninstall:\n%s", b)
	}
	if ok, _, _ := Installed(path); ok {
		t.Fatal("still installed")
	}
}

func TestInstallNewAndEmpty(t *testing.T) {
	for name, content := range map[string]*string{"missing": nil, "empty": ptr(""), "empty object": ptr("{}")} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "hooks.json")
			if content != nil {
				os.WriteFile(path, []byte(*content), 0o600)
			}
			if _, err := Install(path, filepath.Join(dir, "bin", "shiplino"), t.TempDir()); err != nil {
				t.Fatal(err)
			}
			b, _ := os.ReadFile(path)
			if len(ours(t, b)) != len(Events) {
				t.Fatalf("hooks:\n%s", b)
			}
		})
	}
}

func TestInstallLeavesUnparseableAlone(t *testing.T) {
	for name, content := range map[string]string{
		"comments":  "{\n  // mine\n  \"hooks\": {}\n}\n",
		"malformed": `{"hooks": [`,
		"bad hooks": `{"hooks": []}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "hooks.json")
			os.WriteFile(path, []byte(content), 0o600)
			_, err := Install(path, filepath.Join(dir, "bin", "shiplino"), t.TempDir())
			if !errors.Is(err, hookfile.ErrUnparseable) {
				t.Fatalf("err = %v", err)
			}
			if b, _ := os.ReadFile(path); string(b) != content {
				t.Fatalf("file changed:\n%s", b)
			}
		})
	}
}

func TestInstallRelativeBinary(t *testing.T) {
	if _, err := Install(filepath.Join(t.TempDir(), "hooks.json"), "shiplino", t.TempDir()); err == nil {
		t.Fatal("relative binary accepted")
	}
}

func TestHandlerPowerShell(t *testing.T) {
	h := handler(`C:\Users\dev\.shiplino\bin\shiplino.exe`, true)
	ps, _ := h.Get("powershell")
	cmd, _ := h.Get("command")
	if ps != "& "+cmd.(string) || !hookfile.IsOurs(h, Name) {
		t.Fatalf("powershell = %v, command = %v", ps, cmd)
	}
	if _, ok := handler("/usr/local/bin/shiplino", false).Get("powershell"); ok {
		t.Fatal("powershell set off Windows")
	}
}

func ptr(s string) *string { return &s }
