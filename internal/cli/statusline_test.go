package cli

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/elephaant/shiplino/pkg/adapters/claudecode"
)

// The status line wrapper is opt-in: plain setup leaves it alone,
// --statusline wraps the user's command, --no-statusline and uninstall
// put it back exactly, and doctor says which state it's in.
func TestSetupStatusLine(t *testing.T) {
	e, out := testEnv(t)
	t.Setenv("HOME", e.userHome)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", e.userHome)
	}
	ctx := context.Background()
	settings := filepath.Join(e.userHome, ".claude", "settings.json")
	own := `input=$(cat); echo "$input" | jq -r .model.id && echo '<ok>'`
	original := "{\n  \"statusLine\": {\n    \"type\": \"command\",\n    \"command\": \"input=$(cat); echo \\\"$input\\\" | jq -r .model.id && echo '<ok>'\",\n    \"padding\": 1\n  }\n}\n"
	os.MkdirAll(filepath.Dir(settings), 0o700)
	os.WriteFile(settings, []byte(original), 0o600)
	status := func() claudecode.StatusLine {
		t.Helper()
		st, err := claudecode.ReadStatusLine(settings, e.userHome)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}

	if code := setup(ctx, e, []string{"--no-service"}); code != 0 {
		t.Fatalf("setup exit %d:\n%s", code, out)
	}
	if st := status(); st.Installed || st.Other != own || strings.Contains(out.String(), "status line") {
		t.Fatalf("plain setup touched the status line: %+v\n%s", st, out)
	}

	out.Reset()
	before, _ := os.ReadFile(settings)
	if code := setup(ctx, e, []string{"--dry-run", "--statusline"}); code != 0 {
		t.Fatalf("dry run exit %d:\n%s", code, out)
	}
	if after, _ := os.ReadFile(settings); string(after) != string(before) || !strings.Contains(out.String(), " statusline --wrap ") {
		t.Fatalf("dry run:\n%s", out)
	}

	out.Reset()
	if code := setup(ctx, e, []string{"--no-service", "--statusline"}); code != 0 {
		t.Fatalf("setup --statusline exit %d:\n%s", code, out)
	}
	if st := status(); !st.Installed || st.Original != own || st.Bin != e.binPath() || !strings.Contains(out.String(), "records plan usage") {
		t.Fatalf("not wrapped: %+v\n%s", st, out)
	}
	if ok, _, _ := claudecode.Installed(settings); !ok {
		t.Fatal("hooks missing")
	}

	out.Reset()
	doctor(ctx, e, nil)
	if !strings.Contains(out.String(), "Claude Code status line") || !strings.Contains(out.String(), "on: wraps your status line command") {
		t.Fatalf("doctor:\n%s", out)
	}

	out.Reset()
	if code := setup(ctx, e, []string{"--no-service", "--no-statusline"}); code != 0 || !strings.Contains(out.String(), "your own status line command is back") {
		t.Fatalf("setup --no-statusline exit %d:\n%s", code, out)
	}
	if st := status(); st.Installed || st.Other != own {
		t.Fatalf("not restored: %+v", st)
	}

	out.Reset()
	doctor(ctx, e, nil)
	if !strings.Contains(out.String(), "off (opt-in") {
		t.Fatalf("doctor:\n%s", out)
	}

	// Uninstall restores it too, and with the hooks gone the file is the
	// user's original again.
	setup(ctx, e, []string{"--no-service", "--statusline"})
	out.Reset()
	if code := uninstall(ctx, e, nil); code != 0 {
		t.Fatalf("uninstall exit %d:\n%s", code, out)
	}
	if after, _ := os.ReadFile(settings); string(after) != original {
		t.Fatalf("after uninstall:\n%s\nwant\n%s", after, original)
	}
}

// Without a status line of the user's, the wrapper adds an empty one (or
// the minimal line on request), and taking it out removes it.
func TestSetupStatusLineWithoutOne(t *testing.T) {
	e, out := testEnv(t)
	t.Setenv("HOME", e.userHome)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", e.userHome)
	}
	ctx := context.Background()
	settings := filepath.Join(e.userHome, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(settings), 0o700)

	if code := setup(ctx, e, []string{"--no-service", "--statusline=minimal"}); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	st, err := claudecode.ReadStatusLine(settings, e.userHome)
	if err != nil || !st.Installed || !st.Minimal || st.Original != "" {
		t.Fatalf("%+v %v", st, err)
	}
	out.Reset()
	doctor(ctx, e, nil)
	if !strings.Contains(out.String(), "short plan usage line") {
		t.Fatalf("doctor:\n%s", out)
	}
	if code := setup(ctx, e, []string{"--no-service", "--no-statusline"}); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	b, _ := os.ReadFile(settings)
	if strings.Contains(string(b), "statusLine") {
		t.Fatalf("statusLine left:\n%s", b)
	}
}
