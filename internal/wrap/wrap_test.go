package wrap

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/elephaant/shiplino/internal/spool"
	"github.com/elephaant/shiplino/pkg/adapters"
	wrapadapter "github.com/elephaant/shiplino/pkg/adapters/wrap"
	"github.com/elephaant/shiplino/pkg/model"
)

// The test binary doubles as the wrapped command (and, for signal tests,
// as the wrapper): WRAP_TEST_MODE picks the role.
func TestMain(m *testing.M) {
	switch os.Getenv("WRAP_TEST_MODE") {
	case "":
		os.Exit(m.Run())
	case "echo": // copy stdin to stdout, a line to stderr, exit with WRAP_TEST_EXIT
		io.Copy(os.Stdout, os.Stdin)
		fmt.Fprint(os.Stderr, "to stderr")
		code, _ := strconv.Atoi(os.Getenv("WRAP_TEST_EXIT"))
		os.Exit(code)
	case "aider": // append to a chat history like Aider, slower than a poll
		f, _ := os.OpenFile(os.Getenv("WRAP_TEST_HISTORY"), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
		f.WriteString("\n#### add a test  \n\nSure.\n\n")
		time.Sleep(1200 * time.Millisecond)
		f.WriteString("> Tokens: 1.5k sent, 300 received. Cost: $0.02 message, $0.02 session.  \n> Applied edit to t.py  \n")
		f.Close()
		os.Exit(0)
	default:
		helperMode(os.Getenv("WRAP_TEST_MODE"))
	}
}

func setHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("SHIPLINO_HOME", home)
	return home
}

// recorded parses every envelope `shiplino wrap` spooled, through the
// wrap adapter.
func recorded(t *testing.T, home string) []model.Event {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(spool.Dir(home), "wrap", "*.jsonl"))
	if len(files) != 1 {
		t.Fatalf("want one spool file, got %v", files)
	}
	f, err := os.Open(files[0])
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []model.Event
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if len(sc.Bytes()) > spool.MaxLine {
			t.Fatalf("spool line of %d bytes", len(sc.Bytes()))
		}
		var env spool.Envelope
		if err := json.Unmarshal(sc.Bytes(), &env); err != nil || env.Agent != wrapadapter.Name {
			t.Fatalf("envelope %s: %v", sc.Bytes(), err)
		}
		a, _ := adapters.Get(env.Agent)
		evs, err := a.ParseHook(env.P, adapters.HookMeta{EnvelopeID: env.ID, Event: env.Event, ReceivedAt: time.Unix(0, env.TS)})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, evs...)
	}
	return out
}

func self(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

func TestPassesThroughIOAndExitCode(t *testing.T) {
	home := setHome(t)
	t.Setenv("WRAP_TEST_MODE", "echo")
	t.Setenv("WRAP_TEST_EXIT", "3")
	var out, errOut bytes.Buffer
	code := Run([]string{"--agent", "Goose", "--title", "fix login", "--", self(t), "--token=sk-ant-api03-" + strings.Repeat("a", 90)},
		strings.NewReader("hello from stdin"), &out, &errOut)
	if code != 3 || out.String() != "hello from stdin" || errOut.String() != "to stderr" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}

	evs := recorded(t, home)
	start, end := evs[0], evs[len(evs)-1]
	if start.Kind != model.KindSessionStart || start.Agent.Name != "goose" || !strings.HasPrefix(start.SessionID, "goose:") ||
		start.Data["title"] != "fix login" || start.Data["pid"] == nil {
		t.Fatalf("start: %+v", start)
	}
	cmd, _ := start.Data["command"].(string)
	if strings.Contains(cmd, "sk-ant-api03") || !strings.Contains(cmd, "«redacted") {
		t.Fatalf("command not redacted: %q", cmd)
	}
	if wd, _ := os.Getwd(); start.Project == nil || start.Project.CWD != wd {
		t.Fatalf("cwd: %+v", start.Project)
	}
	if end.Kind != model.KindSessionEnd || end.Data["exit_code"] != 3 || end.SessionID != start.SessionID {
		t.Fatalf("end: %+v", end)
	}
}

func TestChildRunsWhenRecordingFails(t *testing.T) {
	// The Shiplino home is a file: nothing can be written under it.
	blocked := filepath.Join(t.TempDir(), "file")
	os.WriteFile(blocked, []byte("x"), 0o600)
	t.Setenv("SHIPLINO_HOME", blocked)
	t.Setenv("WRAP_TEST_MODE", "echo")
	t.Setenv("WRAP_TEST_EXIT", "0")
	var out, errOut bytes.Buffer
	code := Run([]string{"--", self(t)}, strings.NewReader("still runs"), &out, &errOut)
	if code != 0 || out.String() != "still runs" {
		t.Fatalf("code=%d stdout=%q", code, out.String())
	}
	// The user is told once, after the command finished.
	if !strings.HasSuffix(errOut.String(), "\n") || !strings.Contains(errOut.String(), "shiplino wrap: not recorded") {
		t.Fatalf("stderr: %q", errOut.String())
	}
}

func TestPausedRecordsNothing(t *testing.T) {
	home := setHome(t)
	if err := spool.Pause(home, time.Time{}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WRAP_TEST_MODE", "echo")
	var out, errOut bytes.Buffer
	if code := Run([]string{self(t)}, strings.NewReader(""), &out, &errOut); code != 0 || errOut.String() != "to stderr" {
		t.Fatalf("code=%d stderr=%q", code, errOut.String())
	}
	if files, _ := filepath.Glob(filepath.Join(spool.Dir(home), "*", "*")); len(files) != 0 {
		t.Fatalf("paused but recorded: %v", files)
	}
}

func TestUsageErrors(t *testing.T) {
	cases := []struct {
		args []string
		code int
	}{
		{nil, exitUsage},
		{[]string{"--agent"}, exitUsage},
		{[]string{"--bogus", "--", "x"}, exitUsage},
		{[]string{"--help"}, 0},
		{[]string{"--", "shiplino-no-such-command-xyz"}, exitNotFound},
	}
	setHome(t)
	for _, c := range cases {
		var out, errOut bytes.Buffer
		if code := Run(c.args, strings.NewReader(""), &out, &errOut); code != c.code {
			t.Errorf("%v: code %d, want %d (%s)", c.args, code, c.code, errOut.String())
		}
	}
}

func TestParseArgs(t *testing.T) {
	cases := []struct {
		args         []string
		agent, title string
		argv         []string
	}{
		{[]string{"aider", "--model", "x"}, "aider", "", []string{"aider", "--model", "x"}},
		{[]string{"--", "/usr/local/bin/aider"}, "aider", "", []string{"/usr/local/bin/aider"}},
		{[]string{"--agent=aider", "--", "python", "-m", "aider"}, "aider", "", []string{"python", "-m", "aider"}},
		{[]string{"--title", "t", "goose", "run"}, "wrap", "t", []string{"goose", "run"}},
		{[]string{"--agent", "bad/name", "--", "x"}, "wrap", "", []string{"x"}},
	}
	for _, c := range cases {
		agent, title, argv, err := parseArgs(c.args)
		if err != nil || agent != c.agent || title != c.title || strings.Join(argv, " ") != strings.Join(c.argv, " ") {
			t.Errorf("%v: %q %q %v %v", c.args, agent, title, argv, err)
		}
	}
}

func TestAiderHistoryIsRecorded(t *testing.T) {
	home := setHome(t)
	dir := t.TempDir()
	hist := filepath.Join(dir, "chat.md")
	// Earlier runs' lines are not this session's.
	os.WriteFile(hist, []byte("#### an older prompt  \n> Tokens: 9k sent, 1k received. Cost: $1.00 message, $1.00 session.  \n"), 0o600)
	t.Setenv("WRAP_TEST_MODE", "aider")
	t.Setenv("WRAP_TEST_HISTORY", hist)
	var out, errOut bytes.Buffer
	code := Run([]string{"--agent", "aider", "--", self(t), "--chat-history-file", hist}, strings.NewReader(""), &out, &errOut)
	if code != 0 || errOut.Len() != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut.String())
	}
	kinds := []string{}
	var cost float64
	for _, e := range recorded(t, home) {
		kinds = append(kinds, string(e.Kind))
		if !strings.HasPrefix(e.SessionID, "aider:") {
			t.Fatalf("session id %s", e.SessionID)
		}
		if e.Kind == model.KindTurnStart && e.Data["prompt"] != "add a test" {
			t.Fatalf("prompt: %v", e.Data)
		}
		if c, ok := e.Data["total_cost_usd"].(float64); ok {
			cost += c
		}
	}
	got := strings.Join(kinds, " ")
	want := "session.start turn.start usage usage file.edit turn.end session.end"
	if got != want || cost != 0.02 {
		t.Fatalf("events: %s (cost %v), want %s", got, cost, want)
	}
}

func TestAiderMissingHistoryIsReported(t *testing.T) {
	setHome(t)
	t.Setenv("WRAP_TEST_MODE", "echo")
	var out, errOut bytes.Buffer
	missing := filepath.Join(t.TempDir(), "none.md")
	if code := Run([]string{"--agent", "aider", "--", self(t), "--chat-history-file=" + missing}, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(errOut.String(), "no Aider chat history at "+missing) {
		t.Fatalf("stderr: %q", errOut.String())
	}
}
