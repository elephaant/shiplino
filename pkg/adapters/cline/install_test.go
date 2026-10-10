// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

package cline

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeOut makes the test binary stand in for shiplino in the script
// tests: it saves stdin and its arguments to this file and prints noise
// that the script must swallow.
const fakeOut = "CLINE_TEST_FAKE_SHIPLINO_OUT"

func TestMain(m *testing.M) {
	if out := os.Getenv(fakeOut); out != "" {
		in, _ := io.ReadAll(os.Stdin)
		os.WriteFile(out, append([]byte(strings.Join(os.Args[1:], " ")+"\n"), in...), 0o600)
		os.Stdout.WriteString(`{"cancel":true}`)
		os.Stderr.WriteString("noise")
		os.Exit(3)
	}
	os.Exit(m.Run())
}

func TestDetect(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(home string)
		found   bool
		version string
	}{
		{"nothing", func(string) {}, false, ""},
		{"cline dir", func(h string) { os.MkdirAll(filepath.Join(h, ".cline", "data"), 0o700) }, true, ""},
		{"documents", func(h string) { os.MkdirAll(filepath.Join(h, "Documents", "Cline", "Rules"), 0o700) }, true, ""},
		{"vs code extension", func(h string) {
			os.MkdirAll(filepath.Join(h, ".vscode", "extensions", "saoudrizwan.claude-dev-3.9.2"), 0o700)
			os.MkdirAll(filepath.Join(h, ".cursor", "extensions", "saoudrizwan.claude-dev-3.40.0-universal"), 0o700)
		}, true, "3.40.0"},
		{"another extension", func(h string) { os.MkdirAll(filepath.Join(h, ".vscode", "extensions", "golang.go-0.50.0"), 0o700) }, false, ""},
	}
	t.Setenv("PATH", t.TempDir())
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			c.setup(home)
			d := Detect(context.Background(), home)
			if d.Installed != c.found || d.Version != c.version || d.HooksDir != filepath.Join(home, "Documents", "Cline", "Hooks") {
				t.Fatalf("%+v, want found=%v version=%q", d, c.found, c.version)
			}
		})
	}
}

// A temporary home never resolves the real user's Documents folder.
func TestDetectExtensionHermetic(t *testing.T) {
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, ".vscode", "extensions", "saoudrizwan.claude-dev-3.40.0"), 0o700)
	if d := DetectExtension(context.Background(), home); d.Installed || d.HooksDir != HooksDir(home) {
		t.Fatalf("%+v", d)
	}
}

// flavors runs f for the shell and the PowerShell script flavor.
func flavors(t *testing.T, f func(t *testing.T)) {
	for _, ps := range []bool{false, true} {
		name := "sh"
		if ps {
			name = "ps1"
		}
		t.Run(name, func(t *testing.T) {
			old := powerShell
			powerShell = ps
			t.Cleanup(func() { powerShell = old })
			f(t)
		})
	}
}

func TestInstall(t *testing.T) {
	flavors(t, func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, "Documents", "Cline", "Hooks")
		bin := filepath.Join(root, "bin", "shiplino")
		res, err := Install(dir, bin)
		if err != nil || !res.Changed || len(res.Events) != len(Events) || res.Err() != nil {
			t.Fatalf("%+v %v", res, err)
		}
		for _, ev := range Events {
			path := filepath.Join(dir, fileName(ev))
			b, err := os.ReadFile(path)
			if err != nil || !bytes.Contains(b, []byte(" hook --agent cline")) || !bytes.Contains(b, []byte(bin)) {
				t.Fatalf("%s: %v\n%s", ev, err, b)
			}
			if fi, _ := os.Stat(path); runtime.GOOS != "windows" && fi.Mode().Perm()&0o100 == 0 {
				t.Fatalf("%s isn't executable: %v", ev, fi.Mode())
			}
		}
		for _, never := range []string{"PreToolUse", "PreCompact"} {
			if _, err := os.Stat(filepath.Join(dir, fileName(never))); err == nil {
				t.Fatalf("%s registered", never)
			}
		}
		if res, _ := Install(dir, bin); res.Changed || len(res.Events) != len(Events) {
			t.Fatalf("second install: %+v", res)
		}
		if ok, cmd, err := Installed(dir); !ok || err != nil || !strings.Contains(cmd, bin) {
			t.Fatalf("Installed: %v %q %v", ok, cmd, err)
		}
		// A new binary path updates our files in place.
		bin2 := filepath.Join(root, "bin2", "shiplino")
		if res, err := Install(dir, bin2); err != nil || !res.Changed {
			t.Fatalf("%+v %v", res, err)
		}
		if _, cmd, _ := Installed(dir); !strings.Contains(cmd, bin2) {
			t.Fatalf("not updated: %q", cmd)
		}
		if res, err := Uninstall(dir); err != nil || !res.Changed {
			t.Fatalf("uninstall: %+v %v", res, err)
		}
		if ents, _ := os.ReadDir(dir); len(ents) != 0 {
			t.Fatalf("left behind: %v", ents)
		}
		if ok, _, _ := Installed(dir); ok {
			t.Fatal("still installed")
		}
		if res, err := Uninstall(dir); err != nil || res.Changed {
			t.Fatalf("second uninstall: %+v %v", res, err)
		}
	})
}

// The user's own hook files are never touched, and the events they take
// are reported, not silently skipped.
func TestInstallKeepsUserHooks(t *testing.T) {
	flavors(t, func(t *testing.T) {
		dir := t.TempDir()
		mine := filepath.Join(dir, fileName("PostToolUse"))
		other := filepath.Join(dir, "PreToolUse.py")
		os.WriteFile(mine, []byte("#!/bin/sh\necho '{\"cancel\":false}'\n"), 0o755)
		os.WriteFile(other, []byte("print('{}')\n"), 0o644)
		res, err := Install(dir, filepath.Join(dir, "bin", "shiplino"))
		if err != nil || len(res.Events) != len(Events)-1 || len(res.Taken) != 1 || res.Taken[0] != "PostToolUse" {
			t.Fatalf("%+v %v", res, err)
		}
		if !errors.Is(res.Err(), ErrTaken) || !strings.Contains(res.Err().Error(), "PostToolUse") {
			t.Fatalf("err: %v", res.Err())
		}
		if ok, _, err := Installed(dir); !ok || !errors.Is(err, ErrTaken) {
			t.Fatalf("Installed: %v %v", ok, err)
		}
		if _, err := Uninstall(dir); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{mine, other} {
			if _, err := os.Stat(p); err != nil {
				t.Fatalf("user hook removed: %v", err)
			}
		}
		if ents, _ := os.ReadDir(dir); len(ents) != 2 {
			t.Fatalf("left: %v", ents)
		}
	})
}

func TestInstallRelativeBinary(t *testing.T) {
	if _, err := Install(t.TempDir(), "shiplino"); err == nil {
		t.Fatal("relative binary accepted")
	}
}

func TestScriptQuoting(t *testing.T) {
	flavors(t, func(t *testing.T) {
		for _, bin := range []string{"/opt/my tools/shiplino", "/home/o'neil/.shiplino/bin/shiplino", `C:\Users\dev\.shiplino\bin\shiplino.exe`} {
			s := Script(bin)
			if c, ok := ours(s); !ok || !strings.Contains(c, "hook --agent cline") {
				t.Errorf("%s: not recognized: %s", bin, s)
			}
		}
	})
	powerShell = true
	defer func() { powerShell = filepath.Separator == '\\' }()
	if s := Script(`C:\x\shiplino.exe`); !bytes.HasPrefix(s, []byte("\xef\xbb\xbf")) || !bytes.Contains(s, []byte("& 'C:\\x\\shiplino.exe' hook --agent cline *> $null")) {
		t.Errorf("ps1: %s", s)
	}
}

// The installed script, run the way Cline runs it, hands stdin to the
// binary unchanged (UTF-8 included), prints nothing and exits 0 even when
// the binary prints, fails, or is missing.
func TestScriptRuns(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if _, err := Install(dir, self); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, fileName("PostToolUse"))
	run := func(env ...string) (string, error) {
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.Command("powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", script)
		} else {
			cmd = exec.Command("sh", "-c", "'"+script+"'") // the extension runs it through the shell
		}
		cmd.Env = append(os.Environ(), env...)
		cmd.Stdin = strings.NewReader(`{"hookName":"PostToolUse","taskId":"t1","userPromptSubmit":{"prompt":"café ✓"}}`)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	got := filepath.Join(t.TempDir(), "got")
	if out, err := run(fakeOut + "=" + got); out != "" || err != nil {
		t.Fatalf("output %q, err %v", out, err)
	}
	b, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(b), "hook --agent cline\n") || !strings.Contains(string(b), `"prompt":"café ✓"`) {
		t.Fatalf("binary got %q", b)
	}
	// The binary is gone (Shiplino removed without uninstall).
	if _, err := Install(dir, filepath.Join(dir, "missing", "shiplino")); err != nil {
		t.Fatal(err)
	}
	if out, err := run(); out != "" || err != nil {
		t.Fatalf("missing binary: output %q, err %v", out, err)
	}
}
