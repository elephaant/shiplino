// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package daemon

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/elephaant/shiplino/internal/shim"
)

// Run adds new spool folders to the watcher while file events keep
// arriving. On Windows, Watcher.Add waits for the watcher's goroutine,
// which can itself be blocked delivering an event to Run: Run must never
// stop draining events, or the daemon hangs for good.
func TestRunKeepsUpWhileFoldersAppear(t *testing.T) {
	e := newEnv(t)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- e.d.Run(runCtx) }()
	time.Sleep(50 * time.Millisecond) // let the watcher start

	const agents, lines = 40, 25
	var wg sync.WaitGroup
	for a := range agents {
		wg.Go(func() {
			for n := range lines {
				// Each agent writes to its own new folder: a Create event, then
				// an Add, while the other writers keep events coming.
				shim.Run([]string{"--agent", fmt.Sprintf("agent-%02d", a)},
					strings.NewReader(fmt.Sprintf(`{"session_id":"s%d","hook_event_name":"Stop"}`, n%3)))
			}
		})
	}
	wg.Wait()

	deadline := time.Now().Add(20 * time.Second)
	for e.d.Stats().Lines < agents*lines {
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<20)
			t.Fatalf("read %d of %d lines; goroutines:\n%s", e.d.Stats().Lines, agents*lines, buf[:runtime.Stack(buf, true)])
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run didn't return after cancel")
	}
}
