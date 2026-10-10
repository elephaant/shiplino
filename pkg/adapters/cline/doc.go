// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

// Package cline is the Cline adapter: hook registration and hook payload
// parsing, for the VS Code (and JetBrains) extension and for the CLI and
// SDK hosts (CLI, Kanban, Desktop).
//
// Checked on 2026-10-10 against docs.cline.bot (Config, Hooks → SDK
// Plugins) and the source of cline/cline at main (sdk/packages/core/src/
// hooks, sdk/packages/shared/src/hooks/events.ts and storage/paths.ts,
// apps/vscode/src/core/hooks and src/sdk/hooks-adapter.ts).
//
// Hooks are script files, one per event, named after the event, in a
// hooks directory. There is no JSON config to edit. The global directory
// both hosts read is ~/Documents/Cline/Hooks:
//
//   - The extension runs <dir>/<Event> on macOS and Linux (an executable
//     file, run through the shell so its shebang applies) and
//     <dir>/<Event>.ps1 on Windows (powershell -NoProfile -NonInteractive
//     -ExecutionPolicy Bypass -File). It allows one file per event and
//     directory. Its Documents folder is the OS one: xdg-user-dir
//     DOCUMENTS on Linux, the known folder on Windows (often moved into
//     OneDrive), so it can differ from ~/Documents; that location is
//     handled as a second install target.
//   - The CLI/SDK hosts read ~/Documents/Cline/Hooks and ~/.cline/hooks
//     (both, so Shiplino installs into only the first), take any file
//     named <Event>[.sh|.py|.ps1|.js|…] (case-insensitive), and pick the
//     interpreter from the shebang or extension (.ps1: powershell -File on
//     Windows, without -ExecutionPolicy Bypass, so it needs a policy that
//     allows local scripts).
//
// The JSON payload arrives on stdin, in one of two dialects:
//
//	extension: {clineVersion, hookName: "TaskStart"|"PostToolUse"|…,
//	            timestamp (ms since epoch, as a string), taskId,
//	            workspaceRoots, userId, model {provider, slug}, <event object>}
//	CLI/SDK:   {clineVersion, hookName: "agent_start"|"tool_result"|…,
//	            timestamp (RFC 3339), taskId, sessionContext {rootSessionId},
//	            workspaceRoots, workspaceInfo {rootPath, latestGitBranchName},
//	            userId, agent_id, parent_agent_id, <event object(s)>}
//
// The event objects are the same in both (taskStart, userPromptSubmit,
// postToolUse {toolName, parameters (strings; JSON for non-strings),
// result, success, executionTimeMs}, taskComplete, taskCancel); the SDK
// adds tool_result {id, name, input, output, error, durationMs,
// startedAt, endedAt}, error {name, message} and reason.
//
// Output contract: a hook may print JSON {cancel, contextModification,
// errorMessage}. Both hosts treat empty stdout as "no control": the
// extension returns {cancel: false} for exit 0 without JSON
// (hook-factory.ts), and the SDK's parser returns nothing for empty
// stdout (subprocess-runner.ts parseStdout), so nothing is cancelled and
// no context is added. Shiplino's scripts discard all output of the shim
// and always exit 0, even when the binary is gone. Registered:
//
//	TaskStart, TaskResume     session.start (SDK: per run; deduplicated)
//	UserPromptSubmit          turn.start (prompt)
//	PostToolUse               tool.start + tool.end (real duration) + file.read,
//	                          file.edit (± lines), shell.exec, mcp.call
//	TaskComplete              turn.end ok
//	TaskCancel                turn.end interrupted
//	TaskError                 turn.end error (CLI/SDK only)
//	SessionShutdown           session.end (CLI/SDK only)
//
// Never registered: PreToolUse (a gate that runs before every tool call
// and may cancel it; PostToolUse already carries the call and its
// duration) and PreCompact (neither host runs it today).
//
// Token usage and cost: hooks carry none. Cline keeps per-message usage
// and cost (metrics {inputTokens, outputTokens, cacheReadTokens,
// cacheWriteTokens, cost}) in ~/.cline/data/sessions/<id>/<id>.messages.json,
// a pretty-printed JSON document rewritten whole on every save. The
// daemon tails line-based transcripts only, so it isn't read yet; Cline
// sessions show "no cost data". Tested: Cline as of main on 2026-10-10
// (fixtures in testdata/ are synthetic, built from the source's payload
// types).
package cline
