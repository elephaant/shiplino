// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

package daemon

import (
	"fmt"
	"testing"
	"time"
)

// Many session files are parsed in parallel but committed together, in
// batches of maxBatch events, with each file's cursor in the same
// transaction: a restart neither loses nor repeats anything, and each
// session's events apply in order.
func TestManyFilesCommitTogether(t *testing.T) {
	e := newEnv(t)
	const sessions, calls = 40, 10
	for i := range sessions {
		sess := fmt.Sprintf("m%02d", i)
		e.hook(fmt.Sprintf(`{"session_id":%q,"hook_event_name":"UserPromptSubmit","prompt":"go"}`, sess))
		for n := range calls {
			tool := fmt.Sprintf(`"tool_name":"Read","tool_input":{"file_path":"/x/f%d.go"},"tool_use_id":"t%d"`, n, n)
			e.hook(fmt.Sprintf(`{"session_id":%q,"hook_event_name":"PreToolUse",%s}`, sess, tool))
			e.hook(fmt.Sprintf(`{"session_id":%q,"hook_event_name":"PostToolUse",%s,"tool_response":{}}`, sess, tool))
		}
		e.hook(fmt.Sprintf(`{"session_id":%q,"hook_event_name":"Stop"}`, sess))
	}
	var commits, events int
	e.d.OnCommit = func(n int, _ time.Duration) { commits++; events += n }
	e.poll()

	if most := (events + maxBatch - 1) / maxBatch; commits == 0 || commits > most {
		t.Fatalf("%d events from %d files in %d commits, want at most %d", events, sessions, commits, most)
	}
	for i := range sessions {
		s := e.session(fmt.Sprintf("claude-code:m%02d", i))
		if s.ToolCalls != calls || s.InFlight != 0 || s.Turns != 1 || s.NowDoing != "" {
			t.Fatalf("%s out of order or incomplete: tools=%d inflight=%d turns=%d doing=%q", s.ID, s.ToolCalls, s.InFlight, s.Turns, s.NowDoing)
		}
	}
	n := e.eventCount()
	e.restart()
	e.poll()
	if got := e.eventCount(); got != n {
		t.Fatalf("events after restart = %d, want %d", got, n)
	}
	if st := e.d.Stats(); st.Lines != 0 {
		t.Fatalf("restart re-read %d lines", st.Lines)
	}
}
