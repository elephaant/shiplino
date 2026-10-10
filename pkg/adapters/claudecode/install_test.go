// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

package claudecode

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/elephaant/shiplino/pkg/adapters/internal/configfile"
	"github.com/elephaant/shiplino/pkg/adapters/internal/hookfile"
)

// absPath gives a platform-appropriate absolute path for test binaries.
func absPath(unix string) string {
	if runtime.GOOS == "windows" {
		return `C:\Users\dev` + strings.ReplaceAll(unix, "/", `\`) + ".exe"
	}
	return unix
}

var bin = absPath("/home/dev/.shiplino/bin/shiplino")

// userSettings has the user's own hooks and other keys in a deliberate order.
const userSettings = `{
  "model": "opus",
  "hooks": {
    "PreToolUse": [
      { "matcher": "Bash", "hooks": [ { "type": "command", "command": "~/bin/guard.sh" } ] }
    ]
  },
  "permissions": { "allow": ["Bash(git status)"] }
}
`

func setup(t *testing.T, content string) (path, backups string) {
	t.Helper()
	dir := t.TempDir()
	path = filepath.Join(dir, ".claude", "settings.json")
	if content != "" {
		os.MkdirAll(filepath.Dir(path), 0o700)
		os.WriteFile(path, []byte(content), 0o600)
	}
	return path, filepath.Join(dir, "backups")
}

func countOurs(t *testing.T, path string) map[string]int {
	t.Helper()
	b, _ := os.ReadFile(path)
	root, err := configfile.ParseObject(b)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	hooks, _ := root.Get("hooks")
	for _, m := range hooks.(*configfile.Object).Members {
		for _, g := range hookfile.AsList(m.Value) {
			for _, h := range hookfile.AsList(hookfile.Field(g, "hooks")) {
				if hookfile.IsOurs(h, Name) {
					out[m.Key]++
				}
			}
		}
	}
	return out
}

func TestInstallIntoExistingSettings(t *testing.T) {
	path, backups := setup(t, userSettings)
	res, err := Install(path, bin, "2.1.295 (Claude Code)", backups)
	if err != nil || !res.Changed || res.Backup == "" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	ours := countOurs(t, path)
	if len(ours) != len(coreEvents)+len(newerEvents) {
		t.Fatalf("registered for %d events: %v", len(ours), ours)
	}
	for ev, n := range ours {
		if n != 1 {
			t.Errorf("%s: %d entries", ev, n)
		}
	}
	b, _ := os.ReadFile(path)
	s := string(b)
	if !strings.Contains(s, "~/bin/guard.sh") || !strings.Contains(s, `"Bash(git status)"`) {
		t.Fatalf("user content lost:\n%s", s)
	}
	if strings.Index(s, `"model"`) > strings.Index(s, `"hooks"`) || strings.Index(s, `"hooks"`) > strings.Index(s, `"permissions"`) {
		t.Fatalf("key order changed:\n%s", s)
	}
	if !strings.Contains(s, `"args": [`) || !strings.Contains(s, `"async": true`) || !strings.Contains(s, `"timeout": 5`) {
		t.Fatalf("handler shape:\n%s", s)
	}
	if got, _ := os.ReadFile(res.Backup); string(got) != userSettings {
		t.Fatal("backup doesn't match the original")
	}
}

func TestInstallIsIdempotentAndUpdatesInPlace(t *testing.T) {
	path, backups := setup(t, userSettings)
	Install(path, bin, "2.1.295", backups)
	res, err := Install(path, bin, "2.1.295", backups)
	if err != nil || res.Changed {
		t.Fatalf("second install changed the file: %+v %v", res, err)
	}
	// A new binary location replaces the old entries instead of adding more.
	other := absPath("/opt/shiplino/shiplino")
	res, _ = Install(path, other, "2.1.295", backups)
	if !res.Changed {
		t.Fatal("binary path change not applied")
	}
	for ev, n := range countOurs(t, path) {
		if n != 1 {
			t.Fatalf("%s has %d entries after reinstall", ev, n)
		}
	}
	if ok, cmd, _ := Installed(path); !ok || cmd != other {
		t.Fatalf("Installed() = %v %q", ok, cmd)
	}
}

func TestUninstallRestoresUserContent(t *testing.T) {
	path, backups := setup(t, userSettings)
	Install(path, bin, "2.1.295", backups)
	res, err := Uninstall(path, backups)
	if err != nil || !res.Changed {
		t.Fatalf("%+v %v", res, err)
	}
	b, _ := os.ReadFile(path)
	if !hookfile.SameJSON(b, []byte(userSettings)) {
		t.Fatalf("after uninstall:\n%s", b)
	}
	if ok, _, _ := Installed(path); ok {
		t.Fatal("still installed")
	}
	if res, _ := Uninstall(path, backups); res.Changed {
		t.Fatal("second uninstall changed the file")
	}
}

func TestInstallOnMissingAndEmptyFiles(t *testing.T) {
	for name, content := range map[string]string{"missing": "", "empty object": "{}\n", "blank": "  \n"} {
		t.Run(name, func(t *testing.T) {
			path, backups := setup(t, content)
			if content == "  \n" {
				os.MkdirAll(filepath.Dir(path), 0o700)
				os.WriteFile(path, []byte(content), 0o600)
			}
			if _, err := Install(path, bin, "", backups); err != nil {
				t.Fatal(err)
			}
			// Unknown version: only events every version has.
			if ours := countOurs(t, path); len(ours) != len(coreEvents) {
				t.Fatalf("events = %v", ours)
			}
			Uninstall(path, backups)
			b, _ := os.ReadFile(path)
			if strings.Contains(string(b), "hooks") {
				t.Fatalf("empty hooks key left behind: %s", b)
			}
		})
	}
}

func TestOlderVersionGetsCoreEventsOnly(t *testing.T) {
	path, backups := setup(t, "{}")
	Install(path, bin, "1.0.90", backups)
	if ours := countOurs(t, path); len(ours) != len(coreEvents) || ours["StopFailure"] != 0 {
		t.Fatalf("events = %v", ours)
	}
}

func TestUnparseableFileIsNeverTouched(t *testing.T) {
	for name, content := range map[string]string{
		"comments":       "{\n  // my settings\n  \"model\": \"opus\"\n}\n",
		"hooks not obj":  `{"hooks": ["weird"]}`,
		"trailing comma": `{"a": 1,}`,
	} {
		t.Run(name, func(t *testing.T) {
			path, backups := setup(t, content)
			_, err := Install(path, bin, "2.1.295", backups)
			if !errors.Is(err, ErrUnparseable) {
				t.Fatalf("err = %v", err)
			}
			if b, _ := os.ReadFile(path); string(b) != content {
				t.Fatal("file was modified")
			}
			if _, err := os.Stat(backups); !os.IsNotExist(err) {
				t.Fatal("backup dir created for an untouched file")
			}
		})
	}
}

func TestRecognizesOurEntriesOnly(t *testing.T) {
	ours := []string{
		"/home/u/.shiplino/bin/shiplino hook --agent claude-code",
		`"C:\Users\u\.shiplino\bin\shiplino.exe" hook --agent claude-code`,
		"shiplino hook --event Stop --agent=claude-code",
	}
	notOurs := []string{
		"~/bin/guard.sh",
		"shiplino hook --agent codex",
		"/usr/bin/notshiplino hook --agent claude-code",
		"echo shiplino",
	}
	for _, c := range ours {
		if !hookfile.IsOurs(&configfile.Object{Members: []configfile.Member{{Key: "command", Value: c}}}, Name) {
			t.Errorf("not recognized: %s", c)
		}
	}
	for _, c := range notOurs {
		if hookfile.IsOurs(&configfile.Object{Members: []configfile.Member{{Key: "command", Value: c}}}, Name) {
			t.Errorf("wrongly recognized: %s", c)
		}
	}
}

func TestRelativeBinaryRejected(t *testing.T) {
	path, backups := setup(t, "{}")
	if _, err := Install(path, "shiplino", "", backups); err == nil {
		t.Fatal("relative binary path accepted")
	}
}
