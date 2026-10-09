// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

// Command shiplino is the single Shiplino binary: CLI, hook shim and daemon.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/elephaant/shiplino/internal/daemon"
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

	if len(os.Args) < 2 {
		usage()
		return
	}
	switch os.Args[1] {
	case "version", "--version", "-v":
		fmt.Println("shiplino", version)
	case "help", "--help", "-h":
		usage()
	case "daemon":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := daemon.Main(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "shiplino daemon:", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "shiplino: %q is not implemented yet (pre-alpha)\n", os.Args[1])
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`shiplino: flight recorder and kanban board for AI coding agents (pre-alpha)

Usage:
  shiplino <command>

Commands (planned):
  setup       detect agents, register hooks, start the daemon
  daemon      run the background service in the foreground
  doctor      check and fix every agent connection
  status      show running agents per project
  open        open the board in your browser
  projects    list projects
  sprint      show the current sprint
  uninstall   remove Shiplino and its hooks
  version     print the version
`)
}
