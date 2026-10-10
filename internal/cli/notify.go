package cli

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/elephaant/shiplino/internal/config"
	"github.com/elephaant/shiplino/internal/notify"
	"github.com/elephaant/shiplino/internal/notify/push"
)

const notifyUsage = `usage:
  shiplino notify test [desktop|webhook|ntfy|slack|discord]
  shiplino notify add webhook [URL] [--sign]
  shiplino notify add ntfy [--server URL] [--topic NAME] [--token]
  shiplino notify add slack|discord [URL]
  shiplino notify list
  shiplino notify remove webhook|ntfy|slack|discord

Leave the URL out to paste it when asked, so it stays out of your shell history.`

// notifyCmd: desktop notifications and the opt-in push targets.
func notifyCmd(ctx context.Context, e *env, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(e.errOut, notifyUsage)
		return 2
	}
	switch args[0] {
	case "test":
		return notifyTest(ctx, e, args[1:])
	case "add":
		return notifyAdd(e, args[1:])
	case "list":
		return notifyList(e)
	case "remove", "rm":
		if len(args) != 2 || !push.Valid(args[1]) {
			fmt.Fprintln(e.errOut, notifyUsage)
			return 2
		}
		return notifyRemove(e, args[1])
	}
	fmt.Fprintln(e.errOut, notifyUsage)
	return 2
}

// notifyTest shows a sample desktop notification and sends a test alert
// to each push target, so you can check they arrive.
func notifyTest(ctx context.Context, e *env, args []string) int {
	if len(args) > 1 || len(args) == 1 && args[0] != "desktop" && !push.Valid(args[0]) {
		fmt.Fprintln(e.errOut, notifyUsage)
		return 2
	}
	cfg, err := config.Load(e.home)
	if err != nil {
		fmt.Fprintln(e.errOut, err)
		return 1
	}
	only := ""
	if len(args) == 1 {
		only = args[0]
	}
	targets := cfg.Notify.Push.Targets
	if only != "" && only != "desktop" && !slices.Contains(targets, only) {
		fmt.Fprintf(e.errOut, "%s isn't set up; add it with `shiplino notify add %s`.\n", only, only)
		return 1
	}
	failed := 0
	if only == "" || only == "desktop" {
		if code := desktopTest(ctx, e, cfg); code != 0 && (only == "desktop" || len(targets) == 0) {
			return code
		} else if code != 0 {
			failed++
		}
	}
	board := "http://localhost:4777"
	if c, err := e.client(); err == nil {
		board = strings.Replace(c.base, "127.0.0.1", "localhost", 1)
	}
	vault := push.Vault{Home: e.home}
	for _, kind := range targets {
		if only != "" && only != kind {
			continue
		}
		t, _, _, err := vault.Load(kind)
		if err == nil && t == nil {
			err = fmt.Errorf("its details aren't stored; run `shiplino notify add %s` again", kind)
		}
		if err != nil {
			fmt.Fprintf(e.errOut, "%s: %v\n", kind, err)
			failed++
			continue
		}
		tctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		res := push.Send(tctx, *t, []notify.Alert{{Event: notify.EventTest, At: time.Now()}}, board)
		cancel()
		push.Record(e.home, kind, res, 0)
		if !res.OK {
			fmt.Fprintf(e.errOut, "%s (%s): not delivered: %s\n", kind, t.Where(), res.Error)
			failed++
			continue
		}
		fmt.Fprintf(e.out, "Sent a test alert to %s (%s).\n", kind, t.Where())
	}
	if failed > 0 {
		return 1
	}
	return 0
}

func desktopTest(ctx context.Context, e *env, cfg config.Config) int {
	ok, how := notify.Available()
	if e.notifySend != nil {
		ok, how = true, "test sender"
	}
	if !ok {
		fmt.Fprintln(e.errOut, notifyMissingHint())
		return 1
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err := e.send(ctx, notify.Note{Title: "Shiplino notifications work", Body: "You'll hear from Shiplino when an agent is waiting on you, finishes a long turn or fails."})
	if err != nil {
		fmt.Fprintf(e.errOut, "Couldn't show a notification via %s: %v\n", how, err)
		return 1
	}
	fmt.Fprintf(e.out, "Sent a test notification via %s.\n", how)
	if _, on := cfg.NotifySettings(); !on {
		fmt.Fprintf(e.out, "Note: desktop notifications are turned off in %s ([notify] enabled = false).\n", config.Path(e.home))
	}
	switch runtime.GOOS {
	case "darwin":
		fmt.Fprintln(e.out, "Don't see it? Allow notifications for Script Editor in System Settings → Notifications.")
	case "windows":
		fmt.Fprintln(e.out, "Don't see it? Check that notifications for Windows PowerShell are on in Settings → System → Notifications.")
	default:
		fmt.Fprintln(e.out, "Don't see it? Check that Do Not Disturb is off.")
	}
	return 0
}

// notifyAdd stores a target in the keychain and turns it on.
func notifyAdd(e *env, args []string) int {
	if len(args) == 0 || !push.Valid(args[0]) {
		fmt.Fprintln(e.errOut, notifyUsage)
		return 2
	}
	kind := args[0]
	fs := flag.NewFlagSet("notify add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	sign := fs.Bool("sign", false, "")
	server := fs.String("server", "https://ntfy.sh", "")
	topicName := fs.String("topic", "", "")
	withToken := fs.Bool("token", false, "")
	pos, err := parseAnywhere(fs, args[1:])
	if err != nil || len(pos) > 1 || kind == push.Ntfy && len(pos) > 0 || kind != push.Webhook && *sign || kind != push.Ntfy && (*withToken || *topicName != "") {
		fmt.Fprintln(e.errOut, notifyUsage)
		return 2
	}
	in := bufio.NewReader(e.stdin())
	t := push.Target{Kind: kind}
	switch kind {
	case push.Ntfy:
		t.Server, t.Topic = strings.TrimRight(*server, "/"), *topicName
		if t.Topic == "" {
			// Anyone who knows a topic on a public server can read it,
			// so make one nobody can guess.
			t.Topic = "shiplino-" + randomHex(10)
		}
		if *withToken {
			if t.Token = ask(e, in, "Paste the ntfy access token: "); t.Token == "" {
				fmt.Fprintln(e.errOut, "No token given.")
				return 1
			}
		}
	default:
		if len(pos) == 1 {
			t.URL = pos[0]
		} else {
			t.URL = ask(e, in, fmt.Sprintf("Paste the %s URL (it's kept in the OS keychain): ", targetLabel(kind)))
		}
		if *sign {
			t.Secret = randomHex(32)
		}
	}
	if err := t.Validate(); err != nil {
		fmt.Fprintf(e.errOut, "%s: %v\n", kind, err)
		return 1
	}
	where, why, err := push.Vault{Home: e.home}.Save(t)
	if err != nil {
		fmt.Fprintf(e.errOut, "Couldn't store the %s target: %v\n", kind, err)
		return 1
	}
	if err := config.UpdatePush(e.home, func(p *config.Push) {
		if !slices.Contains(p.Targets, kind) {
			p.Targets = append(p.Targets, kind)
		}
	}); err != nil {
		fmt.Fprintln(e.errOut, err)
		return 1
	}
	push.Forget(e.home, kind)
	fmt.Fprintf(e.out, "Added %s (%s).\n", targetLabel(kind), t.Where())
	printStore(e, where, why)
	switch {
	case kind == push.Ntfy:
		fmt.Fprintf(e.out, "In the ntfy app on your phone, subscribe to topic %s", t.Topic)
		if t.Server != "https://ntfy.sh" {
			fmt.Fprintf(e.out, " on server %s", t.Server)
		}
		fmt.Fprintln(e.out, ".")
	case t.Secret != "":
		fmt.Fprintf(e.out, "Signing secret (shown once; your receiver checks X-Shiplino-Signature with it):\n  %s\n", t.Secret)
	}
	fmt.Fprintln(e.out, "Alerts carry metadata only: agent, project, branch, status, why it's waiting, duration, cost and a link to your board.")
	fmt.Fprintf(e.out, "The daemon picks this up within seconds. Try it: shiplino notify test %s\n", kind)
	return 0
}

func notifyList(e *env) int {
	cfg, err := config.Load(e.home)
	if err != nil {
		fmt.Fprintln(e.errOut, err)
		return 1
	}
	targets := cfg.Notify.Push.Targets
	if len(targets) == 0 {
		fmt.Fprintln(e.out, "No push targets. Add one with `shiplino notify add webhook|ntfy|slack|discord`.")
		return 0
	}
	events := cfg.Notify.Push.Events
	if len(events) == 0 {
		events = push.DefaultEvents
	}
	fmt.Fprintf(e.out, "Events: %s\n", strings.Join(events, ", "))
	for _, c := range pushChecks(e.home, targets) {
		mark := "✅"
		if c.warn {
			mark = "⚠️ "
		}
		line := fmt.Sprintf("  %s %-8s %s", mark, strings.TrimPrefix(c.name, "Push "), c.detail)
		if c.warn && c.fixHint != "" {
			line += "  → " + c.fixHint
		}
		fmt.Fprintln(e.out, line)
	}
	return 0
}

// pushChecks describes each push target: where it delivers, where its
// details are kept and how its last delivery went. Doctor shows them too.
func pushChecks(home string, targets []string) []check {
	vault := push.Vault{Home: home}
	status := push.Status(home)
	var out []check
	for _, kind := range targets {
		c := check{ok: true, name: "Push " + kind}
		t, where, why, err := vault.Load(kind)
		switch {
		case err != nil:
			c.warn, c.detail, c.fixHint = true, err.Error(), "shiplino notify add "+kind
			out = append(out, c)
			continue
		case t == nil:
			c.warn, c.detail, c.fixHint = true, "on, but its details aren't stored", "shiplino notify add "+kind
			if why != "" {
				c.detail += " (" + why + ")"
			}
			out = append(out, c)
			continue
		}
		c.detail = t.Where()
		if where == push.InFile {
			c.detail += ", kept in a 0600 file (" + why + ")"
		}
		st, seen := status[kind]
		switch {
		case !seen || st.Last.At.IsZero():
			c.detail += "; nothing sent yet"
			c.fixHint = "shiplino notify test " + kind
		case st.Last.OK:
			c.detail += "; last delivered " + ago(st.Last.At)
		default:
			c.warn = true
			c.detail += "; last delivery failed " + ago(st.Last.At) + ": " + st.Last.Error
			c.fixHint = "shiplino notify test " + kind
			if !st.LastOK.IsZero() {
				c.detail += " (last success " + ago(st.LastOK) + ")"
			}
		}
		if st.Dropped > 0 {
			c.warn = true
			c.detail += fmt.Sprintf("; %d alerts dropped (queue full)", st.Dropped)
		}
		out = append(out, c)
	}
	return out
}

func notifyRemove(e *env, kind string) int {
	if err := (push.Vault{Home: e.home}).Delete(kind); err != nil {
		fmt.Fprintln(e.errOut, err)
		return 1
	}
	if err := config.UpdatePush(e.home, func(p *config.Push) {
		p.Targets = slices.DeleteFunc(p.Targets, func(s string) bool { return s == kind })
	}); err != nil {
		fmt.Fprintln(e.errOut, err)
		return 1
	}
	push.Forget(e.home, kind)
	fmt.Fprintf(e.out, "Removed %s; nothing more is sent there.\n", targetLabel(kind))
	return 0
}

func targetLabel(kind string) string {
	switch kind {
	case push.Ntfy:
		return "ntfy"
	case push.Slack:
		return "Slack webhook"
	case push.Discord:
		return "Discord webhook"
	}
	return "webhook"
}

func printStore(e *env, where, why string) {
	if where == push.InFile {
		fmt.Fprintf(e.out, "No OS keychain here (%s), so it's kept in a file only you can read.\n", why)
		return
	}
	fmt.Fprintln(e.out, "Kept in the OS keychain, not in config.toml.")
}

// ask prompts on stderr and reads one line.
func ask(e *env, in *bufio.Reader, prompt string) string {
	fmt.Fprint(e.errOut, prompt)
	line, _ := in.ReadString('\n')
	return strings.TrimSpace(line)
}

func (e *env) stdin() io.Reader {
	if e.in != nil {
		return e.in
	}
	return os.Stdin
}

// parseAnywhere parses flags that may come after positional arguments.
func parseAnywhere(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func notifyMissingHint() string {
	if runtime.GOOS == "linux" {
		return "No notification service found: install libnotify (e.g. `sudo apt install libnotify-bin`) and make sure a notification daemon runs in your desktop session."
	}
	return "This system has no supported notification service."
}
