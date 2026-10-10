// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

// Package aider reads Aider's chat history file (.aider.chat.history.md).
// Aider has no hooks, so sessions are recorded by `shiplino wrap aider`:
// the wrapper tails the history file during the run and spools the lines
// this package recognizes, and the wrap adapter turns them into events.
//
// Formats checked against Aider's source (aider/io.py, aider/coders/
// base_coder.py, aider/repo.py, aider/args.py; v0.86) on 2026-10-10:
//
//   - user prompts: one "#### <line>" per prompt line (io.user_input)
//   - tool output: "> <text>" blockquotes, with a Markdown hard break
//     ("  ") at the end (io.tool_output → append_chat_history)
//   - announcements: "> Aider v<version>", "> Main model: <name> with
//     <format> edit format" (or "Model:" without a weak/editor model)
//   - usage after each response: "Tokens: 2.1k sent, 1.3k cache write,
//     1.2k cache hit, 512 received." then "Cost: $0.01 message, $0.05
//     session." on the same line, or the next one when both cache figures
//     are present; no Cost part when the model's price is unknown
//   - edits: "> Applied edit to <path>", path relative to the repo root
//   - auto commits: "> Commit <short sha> <message>"
//   - default location: <git root>/.aider.chat.history.md, or
//     --chat-history-file / AIDER_CHAT_HISTORY_FILE
package aider
