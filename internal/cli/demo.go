package cli

import (
	"context"
	"fmt"

	"github.com/elephaant/shiplino/internal/demo"
)

// demoCmd runs a separate daemon with synthetic data until Ctrl-C
// (`shiplino demo`, or `shiplino open --demo`).
func demoCmd(ctx context.Context, e *env, args []string) int {
	open := true
	for _, a := range args {
		switch a {
		case "--no-open":
			open = false
		case "--demo": // from `shiplino open --demo`
		default:
			fmt.Fprintln(e.errOut, "usage: shiplino demo [--no-open]")
			return 2
		}
	}
	cfg := demo.Config{Version: e.version, Out: e.out}
	if open {
		cfg.Open = e.browse
	}
	if err := demo.Run(ctx, cfg); err != nil {
		fmt.Fprintln(e.errOut, "shiplino demo:", err)
		return 1
	}
	return 0
}
