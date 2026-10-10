package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/elephaant/shiplino/internal/report"
	"github.com/elephaant/shiplino/pkg/adapters"
)

const reportUsage = "usage: shiplino report [--since 7d] [--agent <name>] [--project <name>] [--json]"

// reportCmd prints usage straight from the agents' transcripts. It needs
// no setup and writes nothing: no daemon, database, config or hooks.
func reportCmd(ctx context.Context, e *env, args []string) int {
	since, args := flagValue(args, "--since")
	agent, args := flagValue(args, "--agent")
	project, args := flagValue(args, "--project")
	asJSON := false
	for _, a := range args {
		if a != "--json" {
			fmt.Fprintln(e.errOut, reportUsage)
			return 2
		}
		asJSON = true
	}
	window := 7 * 24 * time.Hour
	if since != "" {
		d, err := parseSince(since)
		if err != nil {
			fmt.Fprintln(e.errOut, err)
			return 2
		}
		window = d
	}
	if agent != "" && !slices.Contains(adapters.Names(), agent) {
		fmt.Fprintf(e.errOut, "unknown agent %q (known: %s)\n", agent, strings.Join(adapters.Names(), ", "))
		return 2
	}
	now := time.Now()
	opt := report.Options{UserHome: e.userHome, Since: now.Add(-window), Now: now, Agent: agent, Project: project}
	if f, ok := e.errOut.(*os.File); ok && isTerminal(f) {
		opt.Progress = func(done, total int) {
			fmt.Fprintf(f, "\rReading transcripts %d/%d", done, total)
		}
	}
	r, err := report.Collect(ctx, opt)
	if opt.Progress != nil {
		fmt.Fprint(e.errOut, "\r"+strings.Repeat(" ", 40)+"\r") // clear the progress line
	}
	if err != nil {
		fmt.Fprintln(e.errOut, "shiplino report:", err)
		return 1
	}
	if asJSON {
		enc := json.NewEncoder(e.out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(r); err != nil {
			fmt.Fprintln(e.errOut, err)
			return 1
		}
		return 0
	}
	report.WriteText(e.out, r)
	return 0
}

// parseSince reads a look-back window: "7d", "12h" or "90m".
func parseSince(s string) (time.Duration, error) {
	bad := fmt.Errorf("--since takes a window like 7d, 12h or 90m")
	if len(s) < 2 {
		return 0, bad
	}
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || n < 1 {
		return 0, bad
	}
	switch s[len(s)-1] {
	case 'd':
		return time.Duration(n) * 24 * time.Hour, nil
	case 'h':
		return time.Duration(n) * time.Hour, nil
	case 'm':
		return time.Duration(n) * time.Minute, nil
	}
	return 0, bad
}

// isTerminal reports whether f is an interactive terminal.
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
