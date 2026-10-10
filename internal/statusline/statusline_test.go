package statusline

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/elephaant/shiplino/internal/spool"
)

// The helper process stands in for the user's status line command: it
// echoes its stdin to stdout with a prefix, writes HELPER_ERR to stderr
// and exits HELPER_CODE.
func TestMain(m *testing.M) {
	if os.Getenv("STATUSLINE_HELPER") == "1" {
		in, _ := io.ReadAll(os.Stdin)
		os.Stdout.Write([]byte("\x1b[32mline one\x1b[0m\nline two ✓\n"))
		os.Stdout.Write(in)
		os.Stderr.WriteString(os.Getenv("HELPER_ERR"))
		code, _ := strconv.Atoi(os.Getenv("HELPER_CODE"))
		os.Exit(code)
	}
	os.Exit(m.Run())
}

// helper runs the helper process instead of a shell for the wrapped
// command, so the passthrough is tested the same way on every OS.
func helper(t *testing.T, code int, stderr string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("STATUSLINE_HELPER", "1")
	t.Setenv("HELPER_CODE", strconv.Itoa(code))
	t.Setenv("HELPER_ERR", stderr)
	old := shellFor
	shellFor = func(string) (string, []string) { return exe, nil }
	t.Cleanup(func() { shellFor = old })
}

func wrapArg(cmd string) []string {
	return []string{"--wrap", base64.RawURLEncoding.EncodeToString([]byte(cmd))}
}

// sample is status line input as Claude Code sends it (synthetic).
const sample = `{"session_id":"s-1","version":"2.1.300","cwd":"/home/dev/app","model":{"id":"claude-opus-5-5","display_name":"Opus"},` +
	`"transcript_path":"/home/dev/.claude/projects/app/s-1.jsonl","cost":{"total_cost_usd":0.5},` +
	`"rate_limits":{"five_hour":{"used_percentage":23.5,"resets_at":1738425600},"seven_day":{"used_percentage":41.2,"resets_at":1738857600},` +
	`"spend_limit":{"used_percentage":10,"resets_at":1738857600,"used_usd":3}}}`

func home(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("SHIPLINO_HOME", h)
	return h
}

func spoolLines(t *testing.T, h, session string) []spool.Envelope {
	t.Helper()
	b, err := os.ReadFile(spool.SessionFile(spool.Dir(h), agent, session))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []spool.Envelope
	for _, l := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		var e spool.Envelope
		if err := json.Unmarshal([]byte(l), &e); err != nil {
			t.Fatalf("bad spool line %q: %v", l, err)
		}
		out = append(out, e)
	}
	return out
}

func TestPassthroughIsExact(t *testing.T) {
	for _, code := range []int{0, 1, 3} {
		h := home(t)
		helper(t, code, "warn: something\n")
		var out, errOut bytes.Buffer
		got := Run(wrapArg("my-statusline --flag"), strings.NewReader(sample), &out, &errOut)
		if got != code {
			t.Errorf("exit = %d, want %d", got, code)
		}
		want := "\x1b[32mline one\x1b[0m\nline two ✓\n" + sample
		if out.String() != want {
			t.Errorf("stdout = %q, want %q", out.String(), want)
		}
		if errOut.String() != "warn: something\n" {
			t.Errorf("stderr = %q", errOut.String())
		}
		if lines := spoolLines(t, h, "s-1"); len(lines) != 1 {
			t.Errorf("spool lines = %d, want 1", len(lines))
		}
	}
}

func TestRecordsOnlyLimits(t *testing.T) {
	h := home(t)
	if code := Run(nil, strings.NewReader(sample), io.Discard, io.Discard); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	lines := spoolLines(t, h, "s-1")
	if len(lines) != 1 {
		t.Fatalf("lines = %d", len(lines))
	}
	l := lines[0]
	if l.Agent != "claude-code" || l.Event != "StatusLine" || l.S != "" || l.B != "" {
		t.Errorf("envelope = %+v", l)
	}
	want := `{"hook_event_name":"StatusLine","session_id":"s-1","version":"2.1.300","model":"claude-opus-5-5",` +
		`"rate_limits":{"five_hour":{"used_percentage":23.5,"resets_at":1738425600},"seven_day":{"used_percentage":41.2,"resets_at":1738857600}}}`
	if string(l.P) != want {
		t.Errorf("payload =\n%s\nwant\n%s", l.P, want)
	}
	for _, leak := range []string{"/home/dev", "cost", "spend_limit", "Opus"} {
		if strings.Contains(string(l.P), leak) {
			t.Errorf("payload keeps %q", leak)
		}
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(spool.SessionFile(spool.Dir(h), agent, "s-1"))
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("spool file mode = %v", fi.Mode().Perm())
		}
	}
}

func TestNothingRecorded(t *testing.T) {
	cases := map[string]string{
		"garbage":       "\x00\xff not json {{{",
		"empty":         "",
		"no limits":     `{"session_id":"s-1","model":{"id":"m"}}`,
		"no session":    `{"rate_limits":{"five_hour":{"used_percentage":5}}}`,
		"no percentage": `{"session_id":"s-1","rate_limits":{"five_hour":{"resets_at":1738425600}}}`,
		"wrong types":   `{"session_id":"s-1","rate_limits":{"five_hour":{"used_percentage":"5"}}}`,
		"traversal":     `{"session_id":"../../evil","rate_limits":"x"}`,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			h := home(t)
			helper(t, 0, "")
			var out bytes.Buffer
			if code := Run(wrapArg("x"), strings.NewReader(in), &out, io.Discard); code != 0 {
				t.Errorf("exit = %d", code)
			}
			// The user's command still got the sample, byte for byte.
			if !strings.HasSuffix(out.String(), "line two ✓\n"+in) {
				t.Errorf("stdout = %q", out.String())
			}
			if _, err := os.Stat(spool.Dir(h)); !os.IsNotExist(err) {
				t.Errorf("spool written for %q", name)
			}
		})
	}
}

func TestPausedAndNoHomeStillRunTheCommand(t *testing.T) {
	h := home(t)
	if err := spool.Pause(h, time.Time{}); err != nil {
		t.Fatal(err)
	}
	helper(t, 0, "")
	var out bytes.Buffer
	if code := Run(wrapArg("x"), strings.NewReader(sample), &out, io.Discard); code != 0 || !strings.Contains(out.String(), "line one") {
		t.Errorf("paused: exit %d, out %q", code, out.String())
	}
	if lines := spoolLines(t, h, "s-1"); len(lines) != 0 {
		t.Errorf("recorded while paused")
	}

	// A home Shiplino can't write to: recording fails silently.
	file := filepath.Join(t.TempDir(), "not-a-dir")
	os.WriteFile(file, nil, 0o600)
	t.Setenv("SHIPLINO_HOME", file)
	out.Reset()
	if code := Run(wrapArg("x"), strings.NewReader(sample), &out, io.Discard); code != 0 || !strings.Contains(out.String(), "line one") {
		t.Errorf("bad home: exit %d, out %q", code, out.String())
	}
}

func TestLargeInputPassesThroughUnrecorded(t *testing.T) {
	h := home(t)
	helper(t, 0, "")
	big := `{"session_id":"s-1","rate_limits":{"five_hour":{"used_percentage":5}},"pad":"` + strings.Repeat("x", MaxInput) + `"}`
	var out bytes.Buffer
	if code := Run(wrapArg("x"), strings.NewReader(big), &out, io.Discard); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.HasSuffix(out.String(), big) {
		t.Errorf("the command didn't get the whole input (%d bytes out)", out.Len())
	}
	if lines := spoolLines(t, h, "s-1"); len(lines) != 0 {
		t.Errorf("input over the cap was recorded")
	}
}

func TestNoCommandPrintsNothing(t *testing.T) {
	home(t)
	var out, errOut bytes.Buffer
	if code := Run(nil, strings.NewReader(sample), &out, &errOut); code != 0 || out.Len() != 0 || errOut.Len() != 0 {
		t.Errorf("exit %d, out %q, err %q", code, out.String(), errOut.String())
	}
}

func TestMinimal(t *testing.T) {
	home(t)
	var out bytes.Buffer
	if code := Run([]string{"--minimal"}, strings.NewReader(sample), &out, io.Discard); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if out.String() != "5h 24% · 7d 41%\n" { // resets_at is in the past
		t.Errorf("out = %q", out.String())
	}
	if got := Minimal([]byte(`{"session_id":"s"}`), time.Now()); got != "" {
		t.Errorf("no limits: %q", got)
	}
	resets := time.Now().Add(time.Hour).Unix()
	got := Minimal([]byte(`{"rate_limits":{"five_hour":{"used_percentage":50,"resets_at":`+strconv.FormatInt(resets, 10)+`}}}`), time.Now())
	if want := "5h 50% · resets " + time.Unix(resets, 0).Format("15:04") + "\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBadWrapArgument(t *testing.T) {
	h := home(t)
	var out, errOut bytes.Buffer
	code := Run([]string{"--wrap", "not base64!"}, strings.NewReader(sample), &out, &errOut)
	if code != 1 || out.Len() != 0 || !strings.Contains(errOut.String(), "shiplino doctor") {
		t.Errorf("exit %d, out %q, err %q", code, out.String(), errOut.String())
	}
	if lines := spoolLines(t, h, "s-1"); len(lines) != 1 {
		t.Errorf("limits not recorded")
	}
}

func TestMissingShell(t *testing.T) {
	home(t)
	old := shellFor
	shellFor = func(string) (string, []string) { return filepath.Join(t.TempDir(), "no-such-shell"), nil }
	defer func() { shellFor = old }()
	var out, errOut bytes.Buffer
	if code := Run(wrapArg("x"), strings.NewReader(sample), &out, &errOut); code != 127 || out.Len() != 0 || errOut.Len() == 0 {
		t.Errorf("exit %d, out %q, err %q", code, out.String(), errOut.String())
	}
}

// With a real shell, as Claude Code runs it on macOS and Linux.
func TestRealShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh is the macOS and Linux shell")
	}
	home(t)
	cases := []struct {
		cmd, out, err string
		code          int
	}{
		{`read -r line; printf '%s\n' "$line" | cut -c1-14; printf 'a && <b>\n'`, `{"session_id":` + "\n" + "a && <b>\n", "", 0},
		{`cat >/dev/null; echo oops >&2; exit 7`, "", "oops\n", 7},
		{`no-such-command-for-shiplino-tests`, "", "", 127},
	}
	for _, c := range cases {
		var out, errOut bytes.Buffer
		code := Run(wrapArg(c.cmd), strings.NewReader(sample+"\n"), &out, &errOut)
		if code != c.code || out.String() != c.out || c.err != "" && errOut.String() != c.err {
			t.Errorf("%s: exit %d, out %q, err %q", c.cmd, code, out.String(), errOut.String())
		}
	}
}
