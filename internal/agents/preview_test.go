package agents

import (
	"context"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// tempHome is a fresh home directory; agent env overrides are cleared and
// PATH is empty, so no real agent config is ever found.
func tempHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	for _, k := range []string{"COPILOT_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "GEMINI_CLI_HOME"} {
		t.Setenv(k, "")
	}
	t.Setenv("PATH", "")
	return home
}

func testBin(home string) string {
	name := "shiplino"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(home, ".shiplino", "bin", name)
}

// tree reads every file under dir, with its mode.
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		fi, _ := d.Info()
		if d.IsDir() {
			out[p] = "dir " + fi.Mode().String()
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		out[p] = fi.Mode().String() + " " + string(b)
		return nil
	})
	return out
}

// Every agent's dry run shows a diff of what setup would write, and
// writes nothing: no config, no backups, not one byte changed.
func TestPreviewWritesNothing(t *testing.T) {
	for _, a := range All {
		t.Run(a.Key(), func(t *testing.T) {
			home := tempHome(t)
			_, version, path := a.Detect(context.Background(), home)
			bin, backups := testBin(home), filepath.Join(home, ".shiplino", "backups", a.ID)

			// Nothing installed yet: the diff creates our hook config.
			before := tree(t, home)
			changes, err := a.PreviewInstall(path, bin, version)
			if err != nil || len(changes) == 0 {
				t.Fatalf("preview install: %v %+v", err, changes)
			}
			for _, c := range changes {
				d := c.Diff(c.Path)
				if !c.Created || !strings.HasPrefix(d, "--- /dev/null\n+++ "+c.Path+"\n@@ -0,0 +1") || !strings.Contains(d, a.ID) {
					t.Fatalf("diff for %s:\n%s", c.Path, d)
				}
			}
			if after := tree(t, home); !maps.Equal(before, after) {
				t.Fatalf("preview wrote files:\nbefore %v\nafter  %v", before, after)
			}

			// Install for real; the diff matched what was written.
			if _, _, err := a.Install(path, bin, version, backups); err != nil {
				t.Fatal(err)
			}
			for _, c := range changes {
				if got, err := os.ReadFile(c.Path); err != nil || string(got) != string(c.After) {
					t.Fatalf("install wrote something else to %s: %v\n%s\nwant\n%s", c.Path, err, got, c.After)
				}
			}

			// Installed: setup would change nothing; uninstall would remove it.
			before = tree(t, home)
			if changes, err := a.PreviewInstall(path, bin, version); err != nil || len(changes) != 0 {
				t.Fatalf("preview of an up-to-date install: %v %+v", err, changes)
			}
			changes, err = a.PreviewUninstall(path)
			if err != nil || len(changes) == 0 {
				t.Fatalf("preview uninstall: %v %+v", err, changes)
			}
			for _, c := range changes {
				if d := c.Diff(c.Path); !strings.Contains(d, "\n-") || !strings.Contains(d, a.ID) {
					t.Fatalf("uninstall diff for %s:\n%s", c.Path, d)
				}
			}
			if after := tree(t, home); !maps.Equal(before, after) {
				t.Fatalf("preview wrote files:\nbefore %v\nafter  %v", before, after)
			}
			if _, err := os.Stat(backups); !os.IsNotExist(err) {
				t.Fatalf("backups made: %v", err)
			}
		})
	}
}

func TestPreviewKeepsUserSettings(t *testing.T) {
	home := tempHome(t)
	a, _ := Find("claude-code")
	path := filepath.Join(home, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(path), 0o700)
	user := "{\n  \"model\": \"opus\",\n  \"theme\": \"dark\"\n}\n"
	os.WriteFile(path, []byte(user), 0o600)

	changes, err := a.PreviewInstall(path, testBin(home), "")
	if err != nil || len(changes) != 1 || changes[0].Created || changes[0].Removed {
		t.Fatalf("%v %+v", err, changes)
	}
	d := changes[0].Diff("~/.claude/settings.json")
	for _, want := range []string{"--- ~/.claude/settings.json\n+++ ~/.claude/settings.json\n@@ -1,4 +1,", "   \"model\": \"opus\",\n", "-  \"theme\": \"dark\"\n", "+  \"theme\": \"dark\",\n", "+  \"hooks\": {"} {
		if !strings.Contains(d, want) {
			t.Fatalf("diff lacks %q:\n%s", want, d)
		}
	}
	if b, _ := os.ReadFile(path); string(b) != user {
		t.Fatal("settings changed")
	}

	// A file Shiplino won't edit: no diff, the reason, nothing written.
	commented := "{\n  // mine\n}\n"
	os.WriteFile(path, []byte(commented), 0o600)
	if changes, err := a.PreviewInstall(path, testBin(home), ""); !IsUnparseable(err) || len(changes) != 0 {
		t.Fatalf("commented: %v %+v", err, changes)
	}
	if b, _ := os.ReadFile(path); string(b) != commented {
		t.Fatal("settings changed")
	}
}

func TestKeys(t *testing.T) {
	seen := map[string]bool{}
	for _, a := range All {
		k := a.Key()
		if seen[k] || k == "" {
			t.Fatalf("key %q isn't unique", k)
		}
		seen[k] = true
		if f, ok := Find(k); !ok || f.Name != a.Name {
			t.Fatalf("Find(%q) = %v %v", k, f.Name, ok)
		}
	}
	if !seen["windsurf-jetbrains"] || !seen["cline-vs-code"] || !seen["claude-code"] {
		t.Fatalf("keys: %v", seen)
	}
}

func TestDiff(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after string
		want          string
	}{
		{"equal", "a\nb\n", "a\nb\n", ""},
		{"new file", "", "a\nb\n", "--- /dev/null\n+++ f\n@@ -0,0 +1,2 @@\n+a\n+b\n"},
		{"removed", "a\n", "", "--- f\n+++ /dev/null\n@@ -1 +0,0 @@\n-a\n"},
		{"middle", "1\n2\n3\n4\n5\n6\n7\n8\n9\n", "1\n2\n3\n4\nX\n6\n7\n8\n9\n",
			"--- f\n+++ f\n@@ -2,7 +2,7 @@\n 2\n 3\n 4\n-5\n+X\n 6\n 7\n 8\n"},
		{"two hunks", "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n", "X\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\nY\n",
			"--- f\n+++ f\n@@ -1,4 +1,4 @@\n-1\n+X\n 2\n 3\n 4\n@@ -9,4 +9,4 @@\n 9\n 10\n 11\n-12\n+Y\n"},
		{"no final newline", "a", "a\nb", "--- f\n+++ f\n@@ -1 +1,2 @@\n-a\n\\ No newline at end of file\n+a\n+b\n\\ No newline at end of file\n"},
	} {
		c := Change{Before: []byte(tc.before), After: []byte(tc.after), Created: tc.before == "", Removed: tc.after == ""}
		if tc.before == "" && tc.after == "" {
			c.Created, c.Removed = false, false
		}
		if got := c.Diff("f"); got != tc.want {
			t.Errorf("%s:\n%s\nwant\n%s", tc.name, got, tc.want)
		}
	}
}
