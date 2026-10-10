// Package wrap implements `shiplino wrap -- <command>`: run any CLI agent
// unchanged and record the run (start, end, exit code, and for Aider its
// chat history) through the spool, like the hook shim does for agents
// with hooks. Recording is best effort: the command always runs, with
// the terminal, stdin, stdout and stderr untouched.
package wrap

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/elephaant/shiplino/internal/spool"
	"github.com/elephaant/shiplino/pkg/adapters/aider"
	wrapadapter "github.com/elephaant/shiplino/pkg/adapters/wrap"
	"github.com/elephaant/shiplino/pkg/model"
	"github.com/elephaant/shiplino/pkg/redact"
)

// Usage is the help text for `shiplino wrap`.
const Usage = `Usage: shiplino wrap [--agent NAME] [--title TITLE] -- <command> [args...]

Runs the command exactly as if you typed it (same terminal, input, output
and exit code) and records the run on the board. Aider is detected by its
command name (or --agent aider): its prompts, tokens, cost and edits are
read from its chat history file.
`

// Exit codes for problems starting the command, as shells use them.
const (
	exitUsage       = 2
	exitCannotRun   = 126
	exitNotFound    = 127
	pollEvery       = time.Second
	finishTimeout   = 3 * time.Second
	maxBatchText    = 3000
	maxHistoryChunk = 4 << 20
)

// Run runs the wrapped command and returns its exit code. Only wrap's own
// problems (bad usage, command not found) are printed, to stderr.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	agent, title, argv, err := parseArgs(args)
	if errors.Is(err, errHelp) {
		fmt.Fprint(stdout, Usage)
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "shiplino wrap: %v\n\n%s", err, Usage)
		return exitUsage
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	if cmd.Err != nil {
		fmt.Fprintf(stderr, "shiplino wrap: %v\n", cmd.Err)
		if errors.Is(cmd.Err, exec.ErrNotFound) {
			return exitNotFound
		}
		return exitCannotRun
	}

	// Before the command starts: the history size marks where this run's
	// lines begin.
	rec := newRecorder(agent, title, argv)

	sigs := make(chan os.Signal, 8)
	signal.Notify(sigs, relayed...)
	defer signal.Stop(sigs)

	started := time.Now()
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(stderr, "shiplino wrap: %v\n", err)
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return exitNotFound
		}
		return exitCannotRun
	}
	go rec.run(cmd.Process.Pid)

	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	for {
		select {
		case sig := <-sigs:
			relay(cmd.Process, sig)
			continue
		case <-waited:
		}
		break
	}

	code, sig := exitStatus(cmd.ProcessState)
	if msg := rec.finish(code, sig, time.Since(started)); msg != "" {
		fmt.Fprintln(stderr, "shiplino wrap:", msg)
	}
	return code
}

var errHelp = errors.New("help")

// parseArgs reads wrap's flags up to "--" (or the first non-flag word).
func parseArgs(args []string) (agent, title string, argv []string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			argv = args[i+1:]
			break
		}
		if !strings.HasPrefix(a, "-") {
			argv = args[i:]
			break
		}
		name, val, hasVal := strings.Cut(a, "=")
		switch name {
		case "--agent", "-agent", "--title", "-title":
		case "-h", "--help", "-help":
			return "", "", nil, errHelp
		default:
			return "", "", nil, fmt.Errorf("unknown flag %s", name)
		}
		if !hasVal {
			if i+1 >= len(args) {
				return "", "", nil, fmt.Errorf("%s needs a value", name)
			}
			i++
			val = args[i]
		}
		if strings.HasSuffix(name, "agent") {
			agent = val
		} else {
			title = val
		}
	}
	if len(argv) == 0 {
		return "", "", nil, errors.New("a command to run is required")
	}
	if agent == "" && commandName(argv[0]) == aider.Name {
		agent = aider.Name
	}
	return agentName(agent), title, argv, nil
}

// commandName is the program name without directory or .exe.
func commandName(path string) string {
	base := strings.ToLower(filepath.Base(path))
	return strings.TrimSuffix(base, ".exe")
}

// agentName keeps agent names to what spool paths and session ids allow;
// anything else records under "wrap".
func agentName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || len(s) > 64 {
		return wrapadapter.Name
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return wrapadapter.Name
		}
	}
	return s
}

// recorder writes the run to the spool from its own goroutine, so a slow
// or broken disk never delays the command.
type recorder struct {
	root    string // spool dir; "" when not recording
	start   wrapadapter.Payload
	minimal bool
	hist    *tail

	done   chan struct{}
	end    chan wrapadapter.Payload
	failed string // why recording failed, reported after the run
}

func newRecorder(agent, title string, argv []string) *recorder {
	r := &recorder{done: make(chan struct{}), end: make(chan wrapadapter.Payload, 1)}
	now := time.Now()
	cwd, _ := os.Getwd()
	r.start = wrapadapter.Payload{SessionID: model.NewULID(now), Agent: agent, CWD: cwd}
	home := spool.Home()
	if home == "" {
		r.failed = "not recorded: cannot find your home directory (set SHIPLINO_HOME)"
		return r
	}
	if spool.Paused(home, now) {
		return r // recording is paused on purpose; `shiplino status` says so
	}
	r.root = spool.Dir(home)
	if _, err := os.Stat(filepath.Join(home, spool.MinimalMarker)); err == nil {
		r.minimal = true
	}
	if !r.minimal {
		r.start.Command = redact.Default.Command(commandLine(argv))
		r.start.Title = title
		if title == "" && agent != aider.Name {
			r.start.Title = truncate(r.start.Command, 80)
		}
	}
	if agent == aider.Name {
		path, repo := aider.HistoryPath(argv[1:], os.Getenv, cwd)
		r.hist = &tail{path: path, root: repo}
		if fi, err := os.Stat(path); err == nil {
			r.hist.off = fi.Size()
		}
	}
	return r
}

// run records the start, then follows the Aider history until finish.
func (r *recorder) run(pid int) {
	defer close(r.done)
	if r.root == "" {
		<-r.end
		return
	}
	start := r.start
	start.PID = pid
	if err := r.write(wrapadapter.EventStart, start); err != nil {
		r.failed = "not recorded: " + err.Error()
		<-r.end
		return
	}
	tick := time.NewTicker(pollEvery)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			r.drainHistory()
		case end := <-r.end:
			r.drainHistory()
			if err := r.write(wrapadapter.EventEnd, end); err != nil {
				r.failed = "the end of this run was not recorded: " + err.Error()
			}
			return
		}
	}
}

// finish records the end and waits briefly for the recorder. It returns a
// message for the user when something wasn't recorded.
func (r *recorder) finish(code int, sig string, took time.Duration) string {
	c := code
	r.end <- wrapadapter.Payload{SessionID: r.start.SessionID, Agent: r.start.Agent, CWD: r.start.CWD,
		ExitCode: &c, Signal: sig, DurationMS: took.Milliseconds()}
	select {
	case <-r.done:
	case <-time.After(finishTimeout):
		return "not recorded: writing to the spool timed out"
	}
	if r.failed != "" {
		return r.failed
	}
	if r.hist != nil && r.root != "" && !r.hist.seen {
		return fmt.Sprintf("no Aider chat history at %s; prompts, tokens and cost were not recorded (pass --chat-history-file on the command line if you set it elsewhere)", r.hist.path)
	}
	return ""
}

func (r *recorder) write(event string, p wrapadapter.Payload) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	now := time.Now()
	env := spool.Envelope{ID: model.NewULID(now), Agent: wrapadapter.Name, Event: event, TS: now.UnixNano(), PID: os.Getpid()}
	return spool.Write(r.root, p.SessionID, env, b)
}

// drainHistory spools the relevant history lines appended since the last
// pass, in batches that keep spool lines small.
func (r *recorder) drainHistory() {
	if r.hist == nil {
		return
	}
	lines := r.hist.read()
	for len(lines) > 0 {
		n, size := 0, 0
		for n < len(lines) && (n == 0 || size+len(lines[n].Text) <= maxBatchText) {
			size += len(lines[n].Text)
			n++
		}
		p := wrapadapter.Payload{SessionID: r.start.SessionID, Agent: r.start.Agent, CWD: r.start.CWD, Root: r.hist.root, Lines: lines[:n]}
		if r.minimal {
			for i := range p.Lines {
				p.Lines[i].Text = aider.Minimize(p.Lines[i].Text)
			}
		}
		if err := r.write(wrapadapter.EventHistory, p); err != nil {
			r.failed = "part of this run was not recorded: " + err.Error()
			return
		}
		lines = lines[n:]
	}
}

// tail follows a file from an offset and returns complete new lines that
// Aider's parser cares about.
type tail struct {
	path string
	root string
	off  int64
	seen bool // the file existed at some point during the run
}

func (t *tail) read() []aider.Line {
	f, err := os.Open(t.path)
	if err != nil {
		return nil
	}
	defer f.Close()
	t.seen = true
	fi, err := f.Stat()
	if err != nil {
		return nil
	}
	if fi.Size() < t.off {
		t.off = 0 // replaced or truncated
	}
	buf := make([]byte, min(fi.Size()-t.off, maxHistoryChunk))
	n, _ := f.ReadAt(buf, t.off)
	buf = buf[:n]
	var out []aider.Line
	pos := 0
	for {
		i := bytes.IndexByte(buf[pos:], '\n')
		if i < 0 {
			break
		}
		text := string(buf[pos : pos+i])
		if aider.Relevant(text) {
			out = append(out, aider.Line{Offset: t.off + int64(pos), Len: i + 1, Text: strings.TrimRight(text, "\r")})
		}
		pos += i + 1
	}
	if pos == 0 && n == maxHistoryChunk {
		pos = n // one giant line: skip it rather than stall
	}
	t.off += int64(pos)
	return out
}

// commandLine joins argv for display, quoting words with spaces.
func commandLine(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		if a == "" || strings.ContainsAny(a, " \t\"'") {
			a = `"` + strings.ReplaceAll(a, `"`, `\"`) + `"`
		}
		parts[i] = a
	}
	return strings.Join(parts, " ")
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
