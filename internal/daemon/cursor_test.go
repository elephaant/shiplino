// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package daemon

import (
	"os"
	"strings"
	"testing"

	"github.com/elephaant/shiplino/internal/shim"
)

func TestCursorHooksEndToEnd(t *testing.T) {
	withHome(t)
	e := newEnv(t)
	lines, err := readLines("../../pkg/adapters/cursor/testdata/2026.09/hooks.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range lines {
		shim.Run([]string{"--agent", "cursor"}, strings.NewReader(l))
	}
	// Cursor also runs the Claude Code hook: that copy must be ignored.
	shim.Run([]string{"--agent", "claude-code"}, strings.NewReader(lines[2]))
	e.poll()

	s := e.session("cursor:cv-1")
	if s.Turns != 1 || s.ToolCalls != 4 || s.ToolErrors != 1 || s.LinesAdded != 2 || s.LinesRemoved != 1 {
		t.Fatalf("session: turns=%d tools=%d errs=%d +%d -%d", s.Turns, s.ToolCalls, s.ToolErrors, s.LinesAdded, s.LinesRemoved)
	}
	if s.InputTokens != 1200 || s.CostUSD <= 0 || s.AgentVersion != "2026.09.02" {
		t.Fatalf("usage: in=%d cost=%v version=%q", s.InputTokens, s.CostUSD, s.AgentVersion)
	}
	sub := e.session("cursor:cv-1/sub:sa-9")
	if sub.ParentID != "cursor:cv-1" || sub.ToolCalls != 1 || sub.Status != "done" {
		t.Fatalf("subagent: %+v", sub)
	}
	if _, err := e.st.Session(ctx, "claude-code:cv-1"); err == nil {
		if s, _ := e.st.Session(ctx, "claude-code:cv-1"); s != nil {
			t.Fatal("Cursor-run Claude Code hook created a session")
		}
	}
}

func readLines(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n"), nil
}
