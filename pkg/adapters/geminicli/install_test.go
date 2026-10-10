package geminicli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/elephaant/shiplino/pkg/adapters/internal/configfile"
	"github.com/elephaant/shiplino/pkg/adapters/internal/hookfile"
)

func absPath(unix string) string {
	if runtime.GOOS == "windows" {
		return `C:\Users\dev` + strings.ReplaceAll(unix, "/", `\`) + ".exe"
	}
	return unix
}

var bin = absPath("/home/dev/.shiplino/bin/shiplino")

// userSettings has the user's own hooks and other keys in a deliberate order.
const userSettings = `{
  "general": { "vimMode": true },
  "hooks": {
    "BeforeTool": [
      { "matcher": "write_file|replace", "hooks": [ { "name": "guard", "type": "command", "command": "~/bin/guard.sh", "timeout": 3000 } ] }
    ]
  },
  "security": { "auth": { "selectedType": "oauth-personal" } }
}
`

func setup(t *testing.T, content string) (path, backups string) {
	t.Helper()
	dir := t.TempDir()
	path = filepath.Join(dir, ".gemini", "settings.json")
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
	res, err := Install(path, bin, backups)
	if err != nil || !res.Changed || res.Backup == "" || len(res.Events) != len(Events) {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	ours := countOurs(t, path)
	if len(ours) != len(Events) {
		t.Fatalf("registered for %d events: %v", len(ours), ours)
	}
	for ev, n := range ours {
		if n != 1 {
			t.Errorf("%s: %d entries", ev, n)
		}
	}
	for _, never := range []string{"BeforeModel", "AfterModel", "BeforeToolSelection"} {
		if ours[never] > 0 {
			t.Errorf("registered for %s", never)
		}
	}
	b, _ := os.ReadFile(path)
	s := string(b)
	if !strings.Contains(s, "~/bin/guard.sh") || !strings.Contains(s, `"oauth-personal"`) {
		t.Fatalf("user content lost:\n%s", s)
	}
	if strings.Index(s, `"general"`) > strings.Index(s, `"hooks"`) || strings.Index(s, `"hooks"`) > strings.Index(s, `"security"`) {
		t.Fatalf("key order changed:\n%s", s)
	}
	if !strings.Contains(s, `"type": "command"`) || !strings.Contains(s, `"timeout": 5000`) || !strings.Contains(s, `"name": "shiplino"`) {
		t.Fatalf("handler shape:\n%s", s)
	}
	if got, _ := os.ReadFile(res.Backup); string(got) != userSettings {
		t.Fatal("backup doesn't match the original")
	}
	if ok, cmd, err := Installed(path); !ok || err != nil || !strings.Contains(cmd, bin) {
		t.Fatalf("Installed() = %v %q %v", ok, cmd, err)
	}
}

func TestInstallIsIdempotentAndUpdatesInPlace(t *testing.T) {
	path, backups := setup(t, userSettings)
	Install(path, bin, backups)
	if res, err := Install(path, bin, backups); err != nil || res.Changed {
		t.Fatalf("second install changed the file: %+v %v", res, err)
	}
	other := absPath("/opt/ship lino/shiplino")
	if res, _ := Install(path, other, backups); !res.Changed {
		t.Fatal("binary path change not applied")
	}
	for ev, n := range countOurs(t, path) {
		if n != 1 {
			t.Fatalf("%s has %d entries after reinstall", ev, n)
		}
	}
	if ok, cmd, _ := Installed(path); !ok || !strings.Contains(cmd, other) {
		t.Fatalf("Installed() = %v %q", ok, cmd)
	}
}

func TestUninstallKeepsUserHooks(t *testing.T) {
	path, backups := setup(t, userSettings)
	Install(path, bin, backups)
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
	for name, content := range map[string]string{"missing": "", "empty object": "{}\n"} {
		t.Run(name, func(t *testing.T) {
			path, backups := setup(t, content)
			if res, err := Install(path, bin, backups); err != nil || !res.Changed {
				t.Fatalf("%+v %v", res, err)
			}
			if got := countOurs(t, path); len(got) != len(Events) {
				t.Fatalf("events: %v", got)
			}
			Uninstall(path, backups)
			b, _ := os.ReadFile(path)
			if !hookfile.SameJSON(b, []byte("{}")) {
				t.Fatalf("after uninstall: %s", b)
			}
		})
	}
}

func TestCommentedOrMalformedSettingsAreLeftAlone(t *testing.T) {
	for name, content := range map[string]string{
		// Gemini CLI accepts comments in settings.json; we don't rewrite them.
		"commented":           "{\n  // my theme\n  \"ui\": { \"theme\": \"GitHub\" }\n}\n",
		"malformed":           `{"hooks": {`,
		"hooks not an object": `{"hooks": []}`,
	} {
		t.Run(name, func(t *testing.T) {
			path, backups := setup(t, content)
			_, err := Install(path, bin, backups)
			if !errors.Is(err, hookfile.ErrUnparseable) {
				t.Fatalf("err = %v", err)
			}
			if b, _ := os.ReadFile(path); string(b) != content {
				t.Fatalf("file changed:\n%s", b)
			}
			if _, err := Uninstall(path, backups); !errors.Is(err, hookfile.ErrUnparseable) {
				t.Fatalf("uninstall err = %v", err)
			}
			if entries, _ := os.ReadDir(backups); len(entries) != 0 {
				t.Fatal("backup written for an untouched file")
			}
		})
	}
}

func TestInstalledReportsHooksTurnedOff(t *testing.T) {
	for name, cfg := range map[string]string{
		"system off":    `{"enabled": false}`,
		"ours disabled": `{"disabled": ["shiplino"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			path, backups := setup(t, `{"hooksConfig": `+cfg+`}`)
			Install(path, bin, backups)
			ok, _, err := Installed(path)
			if !ok || !errors.Is(err, ErrHooksOff) {
				t.Fatalf("ok=%v err=%v", ok, err)
			}
		})
	}
	path, backups := setup(t, `{"hooksConfig": {"enabled": true, "disabled": ["other"]}}`)
	Install(path, bin, backups)
	if ok, _, err := Installed(path); !ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

func TestCommand(t *testing.T) {
	cases := []struct{ bin, goos, want string }{
		{"/home/dev/.shiplino/bin/shiplino", "linux", `"/home/dev/.shiplino/bin/shiplino" hook --agent gemini-cli`},
		{`C:\Users\dev's\shiplino.exe`, "windows", `& 'C:\Users\dev''s\shiplino.exe' hook --agent gemini-cli`},
	}
	for _, c := range cases {
		got := Command(c.bin, c.goos)
		if c.goos == "windows" && got != c.want || c.goos != "windows" && runtime.GOOS != "windows" && got != c.want {
			t.Errorf("Command(%q, %s) = %s, want %s", c.bin, c.goos, got, c.want)
		}
		h := &configfile.Object{Members: []configfile.Member{{Key: "command", Value: got}}}
		if !hookfile.IsOurs(h, Name) {
			t.Errorf("%s isn't recognized as ours", got)
		}
	}
}

func TestDetect(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no gemini binary
	t.Setenv("GEMINI_CLI_HOME", "")
	home := t.TempDir()
	if d := Detect(context.Background(), home); d.Installed || d.SettingsPath != filepath.Join(home, ".gemini", "settings.json") {
		t.Fatalf("empty home: %+v", d)
	}
	os.MkdirAll(filepath.Join(home, ".gemini", "other-tool"), 0o700) // another tool's folder isn't Gemini CLI
	if d := Detect(context.Background(), home); d.Installed {
		t.Fatalf("detected from a shared folder: %+v", d)
	}
	os.WriteFile(filepath.Join(home, ".gemini", "settings.json"), []byte("{}"), 0o600)
	if d := Detect(context.Background(), home); !d.Installed {
		t.Fatalf("settings file: %+v", d)
	}
	other := t.TempDir()
	t.Setenv("GEMINI_CLI_HOME", other)
	if d := Detect(context.Background(), home); d.SettingsPath != filepath.Join(other, ".gemini", "settings.json") {
		t.Fatalf("GEMINI_CLI_HOME ignored: %+v", d)
	}
}
