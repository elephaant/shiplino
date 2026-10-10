// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

package copilotcli

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

var t0 = time.Date(2026, 10, 10, 10, 0, 0, 0, time.UTC)

const fixtures = "docs-2026.10"

// Fixture lines are {"event": <--event flag>, "payload": <stdin>}: in the
// camelCase format the payload doesn't name its event.
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
		var line struct {
			Event   string          `json:"event"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatalf("line %d: %v", i+1, err)
		}
		evs, err := Adapter{}.ParseHook(line.Payload, adapters.HookMeta{Event: line.Event, EnvelopeID: fmt.Sprintf("env-%02d", i), ReceivedAt: t0.Add(time.Duration(i) * time.Second)})
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
	if s := by[model.KindSessionStart]; len(s) != 1 || s[0].SessionID != "copilot-cli:cs-1" || !s[0].TS.Equal(time.UnixMilli(1791626400000)) || s[0].Project.CWD != "/home/dev/shop" {
		t.Errorf("session start (agent timestamp): %+v", s)
	}
	if ts := by[model.KindTurnStart]; len(ts) != 1 || ts[0].Data["prompt"] != "fix the cart total rounding" {
		t.Errorf("turn start: %+v", ts)
	}
	starts, ends := by[model.KindToolStart], by[model.KindToolEnd]
	if len(starts) != 8 || len(ends) != 8 {
		t.Fatalf("tools: %d starts, %d ends", len(starts), len(ends))
	}
	for i := range starts {
		if starts[i].Data["tool_call_id"] != ends[i].Data["tool_call_id"] || starts[i].DedupKey == ends[i].DedupKey {
			t.Errorf("tool pair %d: %+v / %+v", i, starts[i], ends[i])
		}
	}
	if starts[0].Data["input_summary"] != "src" || starts[0].Data["tool"] != model.ToolSearch {
		t.Errorf("toolArgs as a JSON string: %+v", starts[0].Data)
	}
	if ends[6].Data["ok"] != false || ends[6].Data["error"] != "network unreachable" {
		t.Errorf("failed tool: %+v", ends[6].Data)
	}
	sh := by[model.KindShellExec]
	if len(sh) != 2 || sh[0].Data["exit_code"] != 1 || sh[1].Data["exit_code"] != 0 || sh[0].Data["command"] != "npm test" {
		t.Errorf("shell: %+v", sh)
	}
	if r := by[model.KindFileRead]; len(r) != 1 || r[0].Data["path"] != "/home/dev/shop/src/cart.ts" {
		t.Errorf("file read: %+v", r)
	}
	fe := by[model.KindFileEdit]
	if len(fe) != 4 {
		t.Fatalf("file edits: %+v", fe)
	}
	want := []struct {
		path, op string
		a, r     int
	}{
		{"/home/dev/shop/src/cart.ts", "modify", 2, 1},
		{"/home/dev/shop/src/cart.test.ts", "create", 2, 0},
		{"/home/dev/shop/src/util.ts", "modify", 2, 1},
		{"/home/dev/shop/src/legacy.ts", "delete", 0, 0},
	}
	for i, w := range want {
		d := fe[i].Data
		if d["path"] != w.path || d["op"] != w.op || d["lines_added"] != w.a || d["lines_removed"] != w.r {
			t.Errorf("file edit %d: %+v, want %+v", i, d, w)
		}
	}
	if w := by[model.KindWaitingStart]; len(w) != 1 || w[0].Data["reason"] != "permission" {
		t.Errorf("waiting (idle notification ignored): %+v", w)
	}
	if se := by[model.KindSubagentEnd]; len(se) != 1 || se[0].Data["child_session_id"] != "copilot-cli:cs-1/sub:ag-7" || se[0].Data["agent_type"] != "explore" {
		t.Errorf("subagent end: %+v", se)
	}
	if er := by[model.KindError]; len(er) != 1 || er[0].Data["message"] != "rate limited" || er[0].Data["recoverable"] != true {
		t.Errorf("error: %+v", er)
	}
	if te := by[model.KindTurnEnd]; len(te) != 1 || te[0].Data["status"] != "ok" {
		t.Errorf("turn end: %+v", te)
	}
	if se := by[model.KindSessionEnd]; len(se) != 1 || se[0].Data["status"] != "ended" || se[0].Data["reason"] != "user_exit" {
		t.Errorf("session end: %+v", se)
	}
	for _, e := range all {
		if b, _ := json.Marshal(e); strings.Contains(string(b), "found two callers") {
			t.Fatal("subagent response stored")
		}
	}
}

func TestParseHookErrors(t *testing.T) {
	meta := adapters.HookMeta{Event: "sessionStart", EnvelopeID: "e", ReceivedAt: t0}
	if _, err := (Adapter{}).ParseHook([]byte(`not json`), meta); err == nil {
		t.Error("bad JSON: want an error")
	}
	if _, err := (Adapter{}).ParseHook([]byte(`{}`), meta); err == nil {
		t.Error("no sessionId: want an error")
	}
	meta.Event = "somethingNew"
	if _, err := (Adapter{}).ParseHook([]byte(`{"sessionId":"s"}`), meta); !errors.Is(err, adapters.ErrUnknownEvent) {
		t.Errorf("unknown event: %v", err)
	}
	// No timestamp: the hook's receive time is used.
	meta.Event = "sessionStart"
	evs, err := (Adapter{}).ParseHook([]byte(`{"sessionId":"s"}`), meta)
	if err != nil || len(evs) != 1 || !evs[0].TS.Equal(t0) {
		t.Errorf("fallback ts: %+v %v", evs, err)
	}
}

func TestInstall(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".copilot", "hooks", HooksFile)
	backups := filepath.Join(dir, "backups")
	bin := filepath.Join(dir, "bin", "shiplino")
	res, err := Install(path, bin, backups)
	if err != nil || !res.Changed || len(res.Events) != len(Events) {
		t.Fatalf("%+v %v", res, err)
	}
	b, _ := os.ReadFile(path)
	if !strings.HasPrefix(strings.TrimSpace(string(b)), "{\n  \"version\": 1,") {
		t.Fatalf("version must come first:\n%s", b)
	}
	var doc struct {
		Version int                         `json:"version"`
		Hooks   map[string][]map[string]any `json:"hooks"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Version != 1 || len(doc.Hooks) != len(Events) {
		t.Fatalf("doc: %+v", doc)
	}
	for ev, hs := range doc.Hooks {
		switch ev {
		case "preToolUse", "permissionRequest", "userPromptTransformed", "subagentStart":
			t.Fatalf("registered a hook that can steer the agent: %s", ev)
		}
		if len(hs) != 1 {
			t.Fatalf("%s: %v", ev, hs)
		}
		h := hs[0]
		bash, _ := h["bash"].(string)
		ps, _ := h["powershell"].(string)
		suffix := " hook --agent copilot-cli --event " + ev
		if h["type"] != "command" || h["timeoutSec"] != float64(5) || h["matcher"] != nil ||
			bash != hookfile.ShellQuote(bin)+suffix || ps != "& "+hookfile.PowerShellQuote(bin)+suffix {
			t.Fatalf("%s: %v", ev, h)
		}
	}

	again, err := Install(path, bin, backups)
	if err != nil || again.Changed {
		t.Fatalf("second install changed the file: %+v %v", again, err)
	}
	if ok, cmd, err := Installed(path); !ok || !strings.Contains(cmd, bin) || err != nil {
		t.Fatalf("Installed: %v %q %v", ok, cmd, err)
	}
	// A new binary path replaces our entries instead of adding more.
	bin2 := filepath.Join(dir, "other", "shiplino")
	if _, err := Install(path, bin2, backups); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	if strings.Count(string(b), "--event sessionStart") != 2 || strings.Contains(string(b), filepath.Join("bin", "shiplino")) {
		t.Fatalf("after reinstall:\n%s", b)
	}

	res, err = Uninstall(path, backups)
	if err != nil || !res.Changed {
		t.Fatalf("uninstall: %+v %v", res, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("our file should be gone: %v", err)
	}
	if res, err := Uninstall(path, backups); err != nil || res.Changed {
		t.Fatalf("uninstall when not installed: %+v %v", res, err)
	}
}

func TestInstallKeepsUserHooks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, HooksFile)
	user := `{"version":1,"disableAllHooks":false,"hooks":{"sessionStart":[{"type":"command","bash":"./log.sh","timeoutSec":10}],"preToolUse":[{"type":"command","bash":"./guard.sh"}]}}`
	os.WriteFile(path, []byte(user), 0o600)
	backups := filepath.Join(dir, "backups")
	res, err := Install(path, filepath.Join(dir, "bin", "shiplino"), backups)
	if err != nil || !res.Changed || res.Backup == "" {
		t.Fatalf("%+v %v", res, err)
	}
	b, _ := os.ReadFile(path)
	for _, want := range []string{"./log.sh", "./guard.sh", "disableAllHooks", "hook --agent copilot-cli --event sessionStart"} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("missing %q:\n%s", want, b)
		}
	}
	if _, err := Uninstall(path, backups); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	if !hookfile.SameJSON(b, []byte(user)) {
		t.Fatalf("uninstall must leave the user's hooks as they were:\n%s", b)
	}
}

func TestInstallLeavesBadFilesAlone(t *testing.T) {
	for name, content := range map[string]string{
		"malformed":           `{"version":1,"hooks":{`,
		"comments":            "{\n  // mine\n  \"version\": 1\n}",
		"hooks not an object": `{"version":1,"hooks":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), HooksFile)
			os.WriteFile(path, []byte(content), 0o600)
			_, err := Install(path, filepath.Join(t.TempDir(), "shiplino"), t.TempDir())
			if !errors.Is(err, hookfile.ErrUnparseable) {
				t.Fatalf("want ErrUnparseable, got %v", err)
			}
			if b, _ := os.ReadFile(path); string(b) != content {
				t.Fatalf("file was changed:\n%s", b)
			}
		})
	}
}

func TestInstallRejectsRelativeBinary(t *testing.T) {
	if _, err := Install(filepath.Join(t.TempDir(), HooksFile), "shiplino", t.TempDir()); err == nil {
		t.Fatal("want an error for a relative binary path")
	}
}

func TestDetect(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COPILOT_HOME", "")
	t.Setenv("PATH", t.TempDir()) // no copilot binary
	d := Detect(context.Background(), home)
	if d.Installed || d.HooksPath != filepath.Join(home, ".copilot", "hooks", HooksFile) {
		t.Fatalf("empty home: %+v", d)
	}
	// An IDE-created ~/.copilot alone isn't Copilot CLI.
	os.MkdirAll(filepath.Join(home, ".copilot", "ide"), 0o700)
	if Detect(context.Background(), home).Installed {
		t.Fatal("~/.copilot/ide alone must not count")
	}
	os.MkdirAll(filepath.Join(home, ".copilot", "session-state"), 0o700)
	if !Detect(context.Background(), home).Installed {
		t.Fatal("session-state should count")
	}
	alt := t.TempDir()
	t.Setenv("COPILOT_HOME", alt)
	if d := Detect(context.Background(), home); d.Installed || d.HooksPath != filepath.Join(alt, "hooks", HooksFile) {
		t.Fatalf("COPILOT_HOME: %+v", d)
	}
}

func TestExitCode(t *testing.T) {
	cases := map[string]struct {
		code  int
		found bool
	}{
		"ok\n<exited with exit code 0>":   {0, true},
		"boom\n<exited with exit code 2>": {2, true},
		"Exit code: 127":                  {127, true},
		"no code here":                    {0, false},
	}
	for in, want := range cases {
		if c, f := exitCode(in); c != want.code || f != want.found {
			t.Errorf("exitCode(%q) = %d %v, want %d %v", in, c, f, want.code, want.found)
		}
	}
}
