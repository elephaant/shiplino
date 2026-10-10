// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

// Command shiplino is the single Shiplino binary: CLI, hook shim and daemon.
package main

import (
	"os"

	"github.com/elephaant/shiplino/internal/cli"
	"github.com/elephaant/shiplino/internal/shim"
)

var version = "0.0.0-dev"

func main() {
	// The hook path runs first, before any other setup, so it stays fast.
	// It must never print and must always exit 0 (docs/how-it-works.md).
	if len(os.Args) > 1 && os.Args[1] == "hook" {
		shim.Run(os.Args[2:], os.Stdin)
		os.Exit(0)
	}
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr, version))
}
