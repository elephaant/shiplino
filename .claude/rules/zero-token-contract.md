---
paths:
  - "cmd/shiplino/**"
  - "internal/shim/**"
  - "internal/spool/**"
  - "pkg/adapters/**"
  - "plugins/**"
---

# Zero-token contract (CI-enforced)

Shiplino's core promise: the model's input and output are byte-for-byte identical with or without Shiplino. Hooks run in the agent **program**, not the model. They only stay free if the shim adds nothing.

## The hook shim (`shiplino hook --agent <name> [--event <name>]`)

1. **No stdout, no stderr. Ever.** Claude Code adds stdout of `SessionStart` / `UserPromptSubmit` hooks to the model context.
2. **Always exit 0.** Exit code 2 means "block" in Claude Code, Codex, Gemini CLI and Windsurf.
3. **No JSON output.** No `decision`, `continue`, `permission`, `additionalContext` or `systemMessage` fields.
4. **No network, no DB, no config parsing, no logging setup.** Read stdin (capped at 8 MB), stat `~/.shiplino/paused`, and append one line to `~/.shiplino/spool/<agent>/<session_id>.jsonl`. That's all.
5. **One `write()` with `O_APPEND`, line ≤ 4 KB.** Bigger payloads go to `spool/blobs/<ulid>.json` and the line references the blob. Spool dir `0700`, files `0600`.
6. **`defer recover()`.** A panic, a full disk, a missing HOME or bad JSON still means silent exit 0.
7. **Fast:** p50 < 3 ms, p99 < 8 ms cold. Keep `internal/shim` free of heavy imports, and check `main` for `hook` before any other init.
8. At capture level `minimal`, the shim drops prompt/output fields before writing.

## Hook registration (adapter installers)

- Only `"type": "command"` hooks. Never `prompt` or `agent` hook types.
- Absolute binary path (GUI apps don't inherit the shell PATH), short timeout (5 s), `async: true` where supported, matcher `*`.
- Never set `failClosed` (Cursor) or anything that makes a failure block the agent.
- Never write to a user project's `CLAUDE.md`, `AGENTS.md`, rules files or system prompts. Never add MCP tools for capture.

## Optional features that do cost tokens

The read-only MCP server and AI summaries are **off by default**. They explain their token cost before enabling and never run inside the user's agent loop.

Verify with the `zero-token-check` skill.
