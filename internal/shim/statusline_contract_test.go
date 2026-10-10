package shim

// The opt-in status line wrapper, `shiplino statusline`, through the real
// binary (built in TestMain). Its output is shown in Claude Code's UI and
// never reaches the model, but it must still show exactly what the
// user's own command shows, and nothing at all without one.

import (
	"bytes"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/elephaant/shiplino/internal/spool"
)

// runStatusLine runs `shiplino statusline` and returns stdout, stderr and
// the exit code.
func runStatusLine(t *testing.T, env []string, stdin []byte, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(binPath, append([]string{"statusline"}, args...)...)
	cmd.Env = env
	cmd.Stdin = bytes.NewReader(stdin)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return out.String(), errOut.String(), code
}

// Without a command of the user's it shows nothing and exits 0 in every
// environment, and it writes only to the spool.
func TestContractStatusLine(t *testing.T) {
	inputs := map[string][]byte{
		"empty":   nil,
		"limits":  []byte(`{"session_id":"s1","rate_limits":{"five_hour":{"used_percentage":12,"resets_at":1738425600}}}`),
		"garbage": []byte("\x00\xff not json {{{"),
		"large":   []byte(`{"session_id":"big","x":"` + strings.Repeat("y", 2<<20) + `"}`),
	}
	file := filepath.Join(t.TempDir(), "not-a-dir")
	_ = os.WriteFile(file, []byte("x"), 0o600)
	home := t.TempDir()
	envs := map[string][]string{"home": baseEnv(home), "no home": {}, "home is a file": baseEnv(file)}
	for envName, env := range envs {
		for name, in := range inputs {
			out, errOut, code := runStatusLine(t, env, in)
			if out != "" || errOut != "" || code != 0 {
				t.Errorf("%s, %s: exit=%d stdout=%q stderr=%q", envName, name, code, out, errOut)
			}
		}
	}
	if b, err := os.ReadFile(spool.SessionFile(spool.Dir(home), "claude-code", "s1")); err != nil || strings.Count(string(b), "\n") != 1 {
		t.Errorf("spool: %v %q", err, b)
	}
	err := filepath.Walk(home, func(p string, info os.FileInfo, err error) error {
		if rel, _ := filepath.Rel(home, p); err == nil && rel != "." && !strings.HasPrefix(rel, "spool") {
			t.Errorf("unexpected path written: %s", rel)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The user's command runs through sh, as Claude Code runs it on macOS and
// Linux: its output, errors and exit code come through unchanged.
func TestContractStatusLineWrapsExactly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Git Bash or PowerShell runs it there; internal/statusline tests the passthrough")
	}
	cmd := `cat >/dev/null; printf '\033[1mbold\033[0m  ✓ a && b\n\nlast'; echo e >&2; exit 4`
	arg := base64.RawURLEncoding.EncodeToString([]byte(cmd))
	env := append(baseEnv(t.TempDir()), "PATH=/usr/bin:/bin")
	out, errOut, code := runStatusLine(t, env, []byte(`{"session_id":"s1"}`), "--wrap", arg)
	if out != "\x1b[1mbold\x1b[0m  ✓ a && b\n\nlast" || errOut != "e\n" || code != 4 {
		t.Errorf("exit=%d stdout=%q stderr=%q", code, out, errOut)
	}
}
