package cli

import (
	"context"
	"fmt"
	"strings"
)

// backfill imports agents' transcripts from before Shiplino was set up.
func backfill(ctx context.Context, e *env, args []string) int {
	since, rest := flagValue(args, "--since")
	if len(rest) > 0 {
		fmt.Fprintln(e.errOut, "usage: shiplino backfill [--since 30d]")
		return 2
	}
	days := 30
	if since != "" {
		n, err := fmt.Sscanf(strings.TrimSuffix(since, "d"), "%d", &days)
		if err != nil || n != 1 || !strings.HasSuffix(since, "d") || days < 1 {
			fmt.Fprintln(e.errOut, "--since takes days, e.g. 30d")
			return 2
		}
	}
	n, err := runBackfill(ctx, e, days)
	if err != nil {
		fmt.Fprintln(e.errOut, err)
		return 1
	}
	fmt.Fprintf(e.out, "Importing %d transcript%s from the last %d days. Sessions appear on the board as they're read.\n", n, plural(n), days)
	return 0
}

func runBackfill(ctx context.Context, e *env, days int) (int, error) {
	c, err := e.client()
	if err != nil {
		return 0, err
	}
	var r struct{ Transcripts int }
	err = c.post(ctx, "/api/v1/backfill", map[string]int{"days": days}, &r)
	return r.Transcripts, err
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
