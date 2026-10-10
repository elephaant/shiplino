// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

package shim

// Zero-token contract: the real binary's hook path must print nothing and
// exit 0 for every input, in every environment. Agents add hook stdout to
// the model context (Claude Code) and treat exit code 2 as "block".

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/elephaant/shiplino/internal/spool"
	"github.com/elephaant/shiplino/pkg/adapters/copilotcli"
)

var binPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "shiplino-contract-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binPath = filepath.Join(dir, "shiplino")
	if runtime.GOOS == "windows" {
		binPath += ".exe"
	}
	build := exec.Command("go", "build", "-o", binPath, "github.com/elephaant/shiplino/cmd/shiplino")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build failed: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// runHook runs `shiplino hook` and returns combined output and exit code.
func runHook(t *testing.T, env []string, stdin []byte, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(binPath, append([]string{"hook"}, args...)...)
	cmd.Env = env
	cmd.Stdin = bytes.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return out.String(), code
}

func baseEnv(home string) []string {
	env := []string{"SHIPLINO_HOME=" + home}
	if runtime.GOOS == "windows" {
		env = append(env, "SystemRoot="+os.Getenv("SystemRoot"))
	}
	return env
}

func TestContractSilentAndExitZero(t *testing.T) {
	agents := []string{"claude-code", "codex", "cursor", "gemini-cli", "copilot-cli", "windsurf", "cline", "opencode", ""}
	inputs := map[string][]byte{
		"empty":     nil,
		"object":    []byte(`{}`),
		"payload":   []byte(`{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"}}`),
		"garbage":   []byte("\x00\xff not json {{{"),
		"array":     []byte(`[1,2,3]`),
		"traversal": []byte(`{"session_id":"../../../../tmp/evil"}`),
		"large":     []byte(`{"session_id":"big","x":"` + strings.Repeat("y", 200000) + `"}`),
	}
	home := t.TempDir()
	for _, agent := range agents {
		for name, in := range inputs {
			args := []string{}
			if agent != "" {
				args = append(args, "--agent", agent)
			}
			out, code := runHook(t, baseEnv(home), in, args...)
			if out != "" || code != 0 {
				t.Errorf("agent=%q input=%s: exit=%d output=%q", agent, name, code, out)
			}
		}
	}
	// Nothing may be written outside the spool directory.
	err := filepath.Walk(home, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(home, p)
		if rel != "." && !strings.HasPrefix(rel, "spool") {
			t.Errorf("unexpected path written: %s", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Every event the Gemini CLI adapter registers, with its real payload
// shape: Gemini CLI parses hook stdout (or stderr) as JSON decisions.
func TestContractGeminiCLIEvents(t *testing.T) {
	base := `"session_id":"g1","transcript_path":"/home/dev/.gemini/tmp/demo/chats/session-x.jsonl","cwd":"/home/dev/demo","timestamp":"2026-10-09T10:00:00.000Z"`
	payloads := map[string]string{
		"SessionStart": `"source":"startup"`,
		"SessionEnd":   `"reason":"exit"`,
		"BeforeAgent":  `"prompt":"add a health endpoint"`,
		"AfterAgent":   `"prompt":"add a health endpoint","prompt_response":"Done.","stop_hook_active":false`,
		"BeforeTool":   `"tool_name":"run_shell_command","tool_input":{"command":"ls"}`,
		"AfterTool":    `"tool_name":"run_shell_command","tool_input":{"command":"ls"},"tool_response":{"llmContent":"Output: a","returnDisplay":"a"}`,
		"Notification": `"notification_type":"ToolPermission","message":"Approve?","details":{"type":"exec"}`,
		"PreCompress":  `"trigger":"auto"`,
	}
	home := t.TempDir()
	for ev, fields := range payloads {
		in := []byte(fmt.Sprintf(`{%s,"hook_event_name":%q,%s}`, base, ev, fields))
		if out, code := runHook(t, baseEnv(home), in, "--agent", "gemini-cli"); out != "" || code != 0 {
			t.Errorf("%s: exit=%d output=%q", ev, code, out)
		}
	}
	f, err := os.Open(spool.SessionFile(spool.Dir(home), "gemini-cli", "g1"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	n := 0
	for sc := bufio.NewScanner(f); sc.Scan(); n++ {
	}
	if n != len(payloads) {
		t.Fatalf("%d spool lines, want %d", n, len(payloads))
	}
}

func TestContractHostileEnvironments(t *testing.T) {
	payload := []byte(`{"session_id":"s1"}`)
	cases := map[string][]string{
		"no home at all":   {},
		"home is a file":   nil, // filled below
		"unwritable spool": nil, // filled below
		"bad flags":        nil,
	}
	file := filepath.Join(t.TempDir(), "not-a-dir")
	_ = os.WriteFile(file, []byte("x"), 0o600)
	cases["home is a file"] = baseEnv(file)

	ro := t.TempDir()
	if runtime.GOOS != "windows" {
		_ = os.MkdirAll(filepath.Join(ro, "spool"), 0o500)
		cases["unwritable spool"] = baseEnv(ro)
	} else {
		delete(cases, "unwritable spool")
	}
	cases["bad flags"] = baseEnv(t.TempDir())

	for name, env := range cases {
		args := []string{"--agent", "claude-code"}
		if name == "bad flags" {
			args = []string{"--agent", "--event", "--nonsense=1", "positional"}
		}
		out, code := runHook(t, env, payload, args...)
		if out != "" || code != 0 {
			t.Errorf("%s: exit=%d output=%q", name, code, out)
		}
	}
}

// Every Windsurf event Shiplino registers, through the real binary.
// Exit code 2 from a pre hook would block Cascade.
func TestContractWindsurfEvents(t *testing.T) {
	home := t.TempDir()
	for _, ev := range []string{"pre_user_prompt", "post_cascade_response", "post_read_code", "post_write_code", "post_run_command", "post_mcp_tool_use"} {
		in := []byte(`{"agent_action_name":"` + ev + `","trajectory_id":"t1","execution_id":"e1","timestamp":"2026-10-10T10:00:00Z","model_name":"Unknown","tool_info":{"file_path":"/app/x.go","user_prompt":"hi","command_line":"ls","cwd":"/app"}}`)
		if out, code := runHook(t, baseEnv(home), in, "--agent", "windsurf"); out != "" || code != 0 {
			t.Errorf("%s: exit=%d output=%q", ev, code, out)
		}
	}
	if lines, err := os.ReadFile(spool.SessionFile(spool.Dir(home), "windsurf", "t1")); err != nil || strings.Count(string(lines), "\n") != 6 {
		t.Errorf("spool: %v\n%s", err, lines)
	}
}

// Many hook processes for one session at once must produce one valid line each.
func TestContractConcurrentProcesses(t *testing.T) {
	home := t.TempDir()
	const procs = 40
	var wg sync.WaitGroup
	for i := 0; i < procs; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			in := fmt.Sprintf(`{"session_id":"shared","n":%d,"pad":%q}`, i, strings.Repeat("p", 3000))
			if out, code := runHook(t, baseEnv(home), []byte(in), "--agent", "claude-code"); out != "" || code != 0 {
				t.Errorf("proc %d: exit=%d output=%q", i, code, out)
			}
		}(i)
	}
	wg.Wait()

	f, err := os.Open(spool.SessionFile(spool.Dir(home), "claude-code", "shared"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, spool.MaxLine*2), spool.MaxLine*2)
	seen := map[float64]bool{}
	for sc.Scan() {
		var e spool.Envelope
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("corrupt line: %v", err)
		}
		var p map[string]any
		if err := json.Unmarshal(e.P, &p); err != nil {
			t.Fatalf("corrupt payload: %v", err)
		}
		seen[p["n"].(float64)] = true
	}
	if len(seen) != procs {
		t.Fatalf("got %d distinct events, want %d", len(seen), procs)
	}
}

// Every Copilot CLI event Shiplino registers, called the way Copilot calls
// it (camelCase payload, event name only in --event), is silent, exits 0
// and lands in the session's spool file.
func TestContractCopilotCLIEvents(t *testing.T) {
	home := t.TempDir()
	for _, ev := range copilotcli.Events {
		in := []byte(`{"sessionId":"cs-1","timestamp":1791626400000,"cwd":"/home/dev/app","toolName":"bash","toolArgs":"{\"command\":\"ls\"}"}`)
		out, code := runHook(t, baseEnv(home), in, "--agent", copilotcli.Name, "--event", ev)
		if out != "" || code != 0 {
			t.Errorf("%s: exit=%d output=%q", ev, code, out)
		}
	}
	b, err := os.ReadFile(spool.SessionFile(spool.Dir(home), copilotcli.Name, "cs-1"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(b), "\n"); n != len(copilotcli.Events) {
		t.Errorf("spool has %d lines, want %d", n, len(copilotcli.Events))
	}
}

// The shim must stay tiny: no network, database or config packages.
func TestShimImportsStayLight(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "github.com/elephaant/shiplino/internal/shim").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"net/http", "net", "database/sql", "modernc.org/sqlite", "github.com/pelletier/go-toml/v2", "log/slog"} {
		for _, dep := range strings.Fields(string(out)) {
			if dep == bad {
				t.Errorf("internal/shim depends on %s", bad)
			}
		}
	}
}
