package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/elephaant/shiplino/internal/daemon"
	"github.com/elephaant/shiplino/internal/spool"
	"github.com/elephaant/shiplino/internal/wrap"
)

const usageText = `shiplino: flight recorder and kanban board for AI coding agents

Usage:
  shiplino <command> [flags]

Commands:
  setup             install Shiplino, connect detected agents, start the daemon
  status            daemon state and what's running right now
  ls                recent sessions (--running, --today)
  open              open the board in your browser
  search <words>    find prompts, commands, files and commits (--project)
  export            sessions as CSV or JSON (--format, --project, --since 7d, --out)
                    or a conversation as Markdown (--session <id>)
  notify test       show a sample desktop notification
  backfill          import agent history from before setup (--since 30d)
  sync              opt-in cloud sync: login, status [--dry-run], allow, deny, logout
  doctor            check everything and explain problems (--fix to repair)
  pause             stop recording (--for 30m); hooks stay installed
  resume            start recording again
  wrap -- <cmd>     run any CLI agent (Aider, ...) and record it (--agent, --title)
  daemon            run the background service in the foreground
  uninstall         remove Shiplino (--purge also deletes all data)
  version           print the version
  help              show this help
`

// Run executes a CLI command and returns the process exit code. The hook
// fast path never gets here; main handles it before anything else.
func Run(args []string, stdout, stderr io.Writer, version string) int {
	if len(args) == 0 {
		fmt.Fprint(stdout, usageText)
		return 0
	}
	// wrap runs another program: it handles its own signals and must run the
	// command even when Shiplino itself can't set up (e.g. no home dir).
	if args[0] == "wrap" {
		return wrap.Run(args[1:], os.Stdin, stdout, stderr)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	e, err := newEnv(stdout, stderr, version)
	if err != nil && args[0] != "version" && args[0] != "help" {
		fmt.Fprintln(stderr, "shiplino:", err)
		return 1
	}
	switch args[0] {
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, "shiplino", version)
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usageText)
	case "setup":
		return setup(ctx, e, args[1:])
	case "uninstall":
		return uninstall(ctx, e, args[1:])
	case "status":
		return status(ctx, e, args[1:])
	case "ls":
		return ls(ctx, e, args[1:])
	case "open":
		return open(ctx, e, args[1:])
	case "search":
		return search(ctx, e, args[1:])
	case "export":
		return export(ctx, e, args[1:])
	case "backfill":
		return backfill(ctx, e, args[1:])
	case "notify":
		return notifyCmd(ctx, e, args[1:])
	case "sync":
		return syncCmd(ctx, e, args[1:])
	case "doctor":
		return doctor(ctx, e, args[1:])
	case "pause":
		return pause(ctx, e, args[1:])
	case "resume":
		return resume(ctx, e, args[1:])
	case "daemon":
		if err := daemon.Main(ctx, version); err != nil {
			fmt.Fprintln(stderr, "shiplino daemon:", err)
			return 1
		}
	default:
		fmt.Fprintf(stderr, "shiplino: unknown command %q\n\n%s", args[0], usageText)
		return 2
	}
	return 0
}

func newEnv(stdout, stderr io.Writer, version string) (*env, error) {
	home := spool.Home()
	userHome, err := os.UserHomeDir()
	if home == "" || err != nil {
		return nil, fmt.Errorf("cannot find your home directory (set SHIPLINO_HOME)")
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return &env{out: stdout, errOut: stderr, home: home, userHome: userHome, self: self, version: version}, nil
}
