---
name: add-adapter
description: Add or update a Shiplino agent adapter (Claude Code, Codex, Cursor, Gemini CLI, Copilot CLI, Windsurf, Cline, OpenCode, Aider, or a new agent). Covers detection, user-level hook install/uninstall, hook payload parsing, transcript tailing, subagent ids, golden fixtures and doctor checks. Use when the user says "support <agent>", "add adapter", "agent X broke after update", or "new hook event".
---

# Add or update an agent adapter

Read `.claude/rules/adapters.md` and `.claude/rules/zero-token-contract.md` first.

## 1. Verify current facts (agents change often)

Fetch the agent's **official** hooks docs and confirm:
- user-level config path(s) per OS, and the format (JSON/TOML/script/plugin)
- the list of hook events, which ones block on exit code or output, async support, and timeout limits
- payload fields: session id, transcript path, cwd, tool name/input/response, subagent id/type, model
- transcript location and the line types that carry token usage
- any trust or approval prompt (e.g. Codex trusts new hooks once)

Write the date checked in the package doc comment.

## 2. Scaffold `pkg/adapters/<agent>/`

| File | Contents |
|------|----------|
| `doc.go` | package doc + verified-on date + `Tested` version range |
| `detect.go` | binary on PATH and/or config dir exists → `Detection{Installed, Version, ConfigPath}` |
| `install.go` | parser-based merge of our entries (absolute bin path, `type: command`, matcher `*`, timeout 5, async where supported). Back up, write atomically, stay idempotent. `Uninstall` removes only entries whose command contains `shiplino hook --agent <agent>` |
| `parse.go` | native event → universal events. Include a mapping table as a comment, and normalize tool names |
| `transcript.go` | optional tailer: tokens/model/titles, `(inode, offset)` |
| `testdata/<version>/` | redacted real payloads + `*.golden.json` |

Register the adapter in `pkg/adapters` (registry) and add its doctor checks.

## 3. Mapping checklist

- `session.start` / `session.end`, `turn.start` / `turn.end`
- `tool.start` / `tool.end` paired by `tool_call_id`, plus `shell.exec`, `file.read`, `file.edit` (with ± lines), `mcp.call`
- `waiting.start` / `waiting.end` (permission prompts, notifications)
- `subagent.start` / `subagent.end` with real ids → `actor_id`, `parent_actor`, `actor_type`
- `usage` from the transcript (input/output/cache read/cache write/reasoning)
- `compact`, `error`
- a `dedup_key` on every event
- unknown events → `raw`, counted, never an error

## 4. Tests

```bash
go test ./pkg/adapters/<agent>/... ./internal/shim/...
```
- install on: missing file, empty `{}`, existing user hooks, comments/odd formatting, invalid file (must be left untouched)
- install twice gives the same result. Uninstall leaves the user's hooks byte-identical.
- golden: fixtures → expected events
- shim contract for every registered event (empty output, exit 0)

## 5. Docs

Add a row to the supported-agents table in `README.md`, plus any user-facing notes (restart needed, trust prompt) in the installer output strings.
