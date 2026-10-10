// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

// Package copilotcli is the GitHub Copilot CLI adapter ("copilot-cli").
//
// Hooks, checked against the official Copilot CLI hooks reference
// (docs.github.com/en/copilot/reference/copilot-cli-reference/cli-hooks-reference)
// and "Using hooks with GitHub Copilot CLI" on 2026-10-10:
//
//   - User hooks are every *.json file in ~/.copilot/hooks/
//     ($COPILOT_HOME/hooks/ when set; %USERPROFILE%\.copilot\hooks\ on
//     Windows). Shiplino owns one file there, shiplino.json, so it never
//     edits a file the user wrote.
//   - Format: {"version": 1, "hooks": {event: [{type: "command", bash,
//     powershell, cwd?, env?, timeoutSec}]}}. Copilot runs bash on macOS
//     and Linux and powershell on Windows; both point at our binary.
//   - Events configured in camelCase get camelCase JSON on stdin with no
//     event name, so each command passes --event <name>. Every payload
//     has sessionId, timestamp (Unix ms) and cwd.
//
// Only hooks whose empty output and failure can't block or steer the
// agent are registered:
//
//	sessionStart          session.start (source: startup|resume|new)
//	sessionEnd            session.end (reason → status)
//	userPromptSubmitted   turn.start (prompt); command-hook output is dropped
//	agentStop             turn.end; empty output means "allow the stop"
//	postToolUse           tool.start + tool.end (+ shell.exec, file.read, file.edit)
//	postToolUseFailure    tool.start + tool.end (failed) (+ shell.exec)
//	subagentStop          subagent.end (child: <session>/sub:<agentId>)
//	preCompact            compact
//	errorOccurred         error
//	notification          waiting.start for permission prompts and dialogs
//
// Never registered: preToolUse and permissionRequest (a crash or non-zero
// exit denies the tool call), userPromptTransformed (can rewrite the
// prompt) and subagentStart (it carries no agent id, so its end couldn't
// be paired; subagent.end creates the child instead).
//
// Limits, by design of the payloads:
//   - There is no tool call id and no pre-tool hook, so each finished tool
//     becomes a start/end pair at the same instant, keyed by the spool
//     envelope id. Tool duration is unknown.
//   - Tool payloads carry no subagent id: a subagent's tool calls are
//     recorded on the parent session.
//   - Hooks carry no model, version or token counts. Copilot keeps its
//     own session log in ~/.copilot/session-state/<id>/events.jsonl, whose
//     format is undocumented; reading it for usage is not done yet.
//   - toolArgs fields are not documented per tool. The parser reads the
//     common names (command, path, old_str/new_str, file_text, patch) and
//     falls back to a plain tool event.
package copilotcli
