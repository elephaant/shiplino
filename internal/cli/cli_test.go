// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package cli

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/elephaant/shiplino/pkg/adapters/claudecode"
	"github.com/elephaant/shiplino/pkg/adapters/codex"
	"github.com/elephaant/shiplino/pkg/adapters/cursor"
)

// builtBinary compiles the real shiplino binary once per test run.
func builtBinary(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "shiplino")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	if b, err := exec.Command("go", "build", "-o", out, "github.com/elephaant/shiplino/cmd/shiplino").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, b)
	}
	return out
}

func testEnv(t *testing.T) (*env, *bytes.Buffer) {
	t.Helper()
	user := t.TempDir()
	bin := builtBinary(t)
	t.Setenv("PATH", "") // never find a real `claude` binary
	var out bytes.Buffer
	// Never touch the real service manager from tests.
	fake := func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
	return &env{out: &out, errOut: &out, home: filepath.Join(user, ".shiplino"), userHome: user, self: bin, version: "test", svcRun: fake}, &out
}

func TestSetupAndUninstall(t *testing.T) {
	e, out := testEnv(t)
	os.MkdirAll(filepath.Join(e.userHome, ".claude"), 0o700) // Claude Code "installed"

	if code := setup(context.Background(), e, []string{"--no-service"}); code != 0 {
		t.Fatalf("setup exit %d:\n%s", code, out)
	}
	if !strings.Contains(out.String(), "✅ Claude Code") || !strings.Contains(out.String(), "✅ Hook test") {
		t.Fatalf("output:\n%s", out)
	}
	settings := filepath.Join(e.userHome, ".claude", "settings.json")
	ok, cmd, err := claudecode.Installed(settings)
	if err != nil || !ok || cmd != e.binPath() {
		t.Fatalf("installed=%v cmd=%q err=%v", ok, cmd, err)
	}
	if fi, err := os.Stat(e.binPath()); err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm()&0o100 == 0) {
		t.Fatalf("binary not installed executable: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.home, "token")); err != nil {
		t.Fatal("token not created")
	}
	if entries, _ := os.ReadDir(filepath.Join(e.home, "spool")); len(entries) != 0 {
		t.Fatalf("self-test left spool data: %v", entries)
	}

	// The installed hook really works from the agent's point of view.
	hook := exec.Command(e.binPath(), "hook", "--agent", "claude-code")
	hook.Env = append(os.Environ(), "SHIPLINO_HOME="+e.home)
	hook.Stdin = strings.NewReader(`{"session_id":"s1","hook_event_name":"Stop"}`)
	if b, err := hook.CombinedOutput(); err != nil || len(b) != 0 {
		t.Fatalf("hook: %v %q", err, b)
	}

	// Setup again: nothing changes.
	out.Reset()
	setup(context.Background(), e, []string{"--no-service"})
	if !strings.Contains(out.String(), "already up to date") {
		t.Fatalf("second setup:\n%s", out)
	}

	out.Reset()
	if code := uninstall(context.Background(), e, nil); code != 0 {
		t.Fatalf("uninstall exit %d:\n%s", code, out)
	}
	if ok, _, _ := claudecode.Installed(settings); ok {
		t.Fatal("hooks still present")
	}
	if _, err := os.Stat(e.home); err != nil {
		t.Fatal("data deleted without --purge")
	}
	uninstall(context.Background(), e, []string{"--purge"})
	if _, err := os.Stat(e.home); !os.IsNotExist(err) {
		t.Fatal("--purge kept data")
	}
}

func TestSetupWithoutAgents(t *testing.T) {
	e, out := testEnv(t)
	if code := setup(context.Background(), e, []string{"--no-service"}); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if !strings.Contains(out.String(), "not found") || !strings.Contains(out.String(), "No supported agents") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestSetupLeavesCommentedSettingsAlone(t *testing.T) {
	e, out := testEnv(t)
	settings := filepath.Join(e.userHome, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(settings), 0o700)
	content := "{\n  // mine\n  \"model\": \"opus\"\n}\n"
	os.WriteFile(settings, []byte(content), 0o600)
	if code := setup(context.Background(), e, []string{"--no-service"}); code == 0 {
		t.Fatal("setup reported success")
	}
	if b, _ := os.ReadFile(settings); string(b) != content {
		t.Fatal("settings modified")
	}
	if !strings.Contains(out.String(), "left untouched") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestRunDispatch(t *testing.T) {
	var out bytes.Buffer
	if code := Run([]string{"version"}, &out, &out, "1.2.3"); code != 0 || !strings.Contains(out.String(), "1.2.3") {
		t.Fatalf("version: %d %q", code, out.String())
	}
	out.Reset()
	if code := Run([]string{"nope"}, &out, &out, "x"); code != 2 {
		t.Fatalf("unknown command exit %d", code)
	}
}

func TestSetupStartsServiceAndWaitsForHealth(t *testing.T) {
	e, out := testEnv(t)
	os.MkdirAll(filepath.Join(e.userHome, ".claude"), 0o700)
	// A fake service manager that "starts" a real daemon in-process.
	srv := startFakeDaemon(t, e.home)
	defer srv.Close()
	if code := setup(context.Background(), e, nil); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if !strings.Contains(out.String(), "✅ Daemon") || !strings.Contains(out.String(), "Nothing else to do") {
		t.Fatalf("output:\n%s", out)
	}
}

// startFakeDaemon serves /api/v1/health and writes the port file, like a
// daemon the service manager would have started.
func startFakeDaemon(t *testing.T, home string) *http.Server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(home, 0o700)
	os.WriteFile(filepath.Join(home, "port"), []byte(fmt.Sprint(ln.Addr().(*net.TCPAddr).Port)), 0o600)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })}
	go srv.Serve(ln)
	return srv
}

func TestSetupCodex(t *testing.T) {
	e, out := testEnv(t)
	os.MkdirAll(filepath.Join(e.userHome, ".codex"), 0o700) // Codex "installed"

	if code := setup(context.Background(), e, []string{"--no-service"}); code != 0 {
		t.Fatalf("setup exit %d:\n%s", code, out)
	}
	if !strings.Contains(out.String(), "✅ Codex") || !strings.Contains(out.String(), "/hooks") {
		t.Fatalf("output:\n%s", out)
	}
	hooks := filepath.Join(e.userHome, ".codex", "hooks.json")
	if ok, cmd, _ := codex.Installed(hooks); !ok || !strings.Contains(cmd, e.binPath()) {
		t.Fatalf("installed=%v cmd=%q", ok, cmd)
	}

	out.Reset()
	doctor(context.Background(), e, nil)
	if !strings.Contains(out.String(), "Codex") || strings.Contains(out.String(), "hooks missing") {
		t.Fatalf("doctor:\n%s", out)
	}

	out.Reset()
	uninstall(context.Background(), e, nil)
	if ok, _, _ := codex.Installed(hooks); ok {
		t.Fatalf("codex hooks still present:\n%s", out)
	}
}

func TestSetupCursor(t *testing.T) {
	e, out := testEnv(t)
	os.MkdirAll(filepath.Join(e.userHome, ".cursor"), 0o700)
	if code := setup(context.Background(), e, []string{"--no-service"}); code != 0 {
		t.Fatalf("setup exit %d:\n%s", code, out)
	}
	hooks := filepath.Join(e.userHome, ".cursor", "hooks.json")
	if ok, cmd, _ := cursor.Installed(hooks); !ok || !strings.Contains(cmd, e.binPath()) {
		t.Fatalf("installed=%v cmd=%q\n%s", ok, cmd, out)
	}
	uninstall(context.Background(), e, nil)
	if ok, _, _ := cursor.Installed(hooks); ok {
		t.Fatal("cursor hooks still present")
	}
}
