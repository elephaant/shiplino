---
paths:
  - "pkg/adapters/**"
---

# Agent adapters

Each adapter lives in `pkg/adapters/<agent>/` and implements the `Adapter` interface: `Name`, `Detect`, `Install`, `Uninstall`, `Check`, `ParseHook`, `TranscriptRoots`, `ParseTranscriptLine`.

Files: `detect.go`, `install.go`, `parse.go`, `transcript.go` (optional), `testdata/<agent-version>/`.

## Rules

- **User-level config only** (e.g. `~/.claude/settings.json`, `~/.codex/hooks.json`, `~/.cursor/hooks.json`). Never edit project files, so we don't dirty user repos.
- **Our entries are recognized by their command string**, `shiplino hook --agent <name>`. Install is idempotent: it updates our entry in place and never duplicates it. Uninstall removes only our entries and never restores old backups.
- **Editing config:** use a real parser (JSON/TOML) and preserve the user's other keys and formatting where possible. Back up to `~/.shiplino/backups/<agent>/<ts>-<file>` first, then write temp → fsync → rename. If the file doesn't parse, **don't touch it**: report it through `Check`.
- **Session ids are namespaced:** `claude-code:<id>`, `codex:<id>`, `cursor:<id>`.
- **Subagents:** use the agent's own ids (`actor_id = session_id + "/sub:" + agent_id`, `parent_actor`). Never infer from timing when an id exists. If you must infer, mark `attribution: "inferred"`.
- **Tool calls:** pair `tool.start`/`tool.end` by `tool_call_id`, never by order. Normalize tool names to `edit|write|read|shell|search|web|mcp|task|other` and keep `tool_raw`.
- **Dedup:** every event gets a stable `dedup_key`. Hooks win for timing, and transcripts win for content and usage.
- **Transcripts:** read-only, from the last offset, tracking `(inode, offset)`. Prefer `transcript_path` from hook payloads over guessing paths.
- **Version pinning:** declare `Tested` version ranges. Newer versions trigger a soft warning in `doctor`, never a failure.
- **Facts drift.** Re-check hook names, config paths and payload fields against the agent's official docs before coding, and put the date checked in the adapter's doc comment.

Tests required: install/uninstall on empty, existing, commented and malformed configs; golden fixtures (redacted) → expected events; the shim contract test for every event the adapter registers. Use the `add-adapter` skill.
