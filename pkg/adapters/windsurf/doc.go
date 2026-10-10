// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

// Package windsurf is the Windsurf (Cascade) adapter: hook registration
// and hook payload parsing.
//
// Hooks, checked against https://docs.windsurf.com/windsurf/cascade/hooks
// (now served as the Devin Desktop "Cascade Hooks" page) on 2026-10-10:
// the user-level file is ~/.codeium/windsurf/hooks.json (the JetBrains
// plugin uses ~/.codeium/hooks.json and isn't handled yet), shaped
// {"hooks": {event: [{command, powershell?, show_output?,
// working_directory?}]}}. `command` runs via `bash -c`; on Windows
// `powershell` runs via `powershell -Command`. There is no timeout or
// async option. Payloads arrive as JSON on stdin with agent_action_name,
// trajectory_id (the conversation), execution_id (the turn), timestamp,
// model_name (a display name, "Unknown" when unknown) and tool_info.
//
// Exit code 2 from a pre hook blocks the action; exit 0 (and any other
// code) lets it proceed, and stdout is only shown in the UI when
// show_output is true. Shiplino registers post hooks only, plus
// pre_user_prompt, which is the only way to see a turn start: the shim
// always exits 0 and prints nothing, so it can't block. Registered:
//
//	pre_user_prompt          turn.start (prompt) + session.update (model)
//	post_cascade_response    turn.end
//	post_read_code           tool.start + tool.end + file.read
//	post_write_code          tool.start + tool.end + file.edit (± lines from edits)
//	post_run_command         tool.start + tool.end + shell.exec
//	post_mcp_tool_use        tool.start + tool.end + mcp.call
//
// Never registered: pre_read_code, pre_write_code, pre_run_command and
// pre_mcp_tool_use (they exist to block), post_cascade_response_with_transcript
// (it makes Windsurf write full transcripts to ~/.windsurf/transcripts) and
// post_setup_worktree (it runs inside a new worktree to set it up).
//
// Windsurf has no session start/end hooks, no subagents, and reports no
// tool call ids, durations, exit codes or token usage in hooks. Its own
// conversation store (~/.codeium/windsurf/cascade/*.pb) is an opaque
// binary format, so there is no transcript tailing. Sessions end by going
// idle. Tested: Windsurf hooks as documented on 2026-10-10 (fixtures in
// testdata/ are synthetic, built from the documented payloads).
package windsurf
