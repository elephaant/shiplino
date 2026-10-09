# Event format (v1)

Every adapter converts its agent's native hooks and transcripts into this format. The JSON Schema is in [schema/event.v1.json](../schema/event.v1.json).

```jsonc
{
  "id": "01JD3K…",                        // ULID
  "v": 1,
  "ts": "2026-10-07T10:22:31.512Z",
  "kind": "tool.end",
  "agent": { "name": "claude-code", "version": "2.4.1", "surface": "cli" },
  "collector": "hook",                     // hook | transcript | git | otlp | http | wrap
  "session_id": "claude-code:3f2c…",       // namespaced by agent
  "actor_id": "claude-code:3f2c…/sub:a91e",// session or subagent that did it
  "parent_actor": "claude-code:3f2c…",
  "project": { "remote": "github.com/acme/api", "branch": "fix/auth", "cwd": "/home/alex/code/api" },
  "data": { "tool_call_id": "toolu_01…", "ok": true, "duration_ms": 812 },
  "dedup_key": "claude-code:3f2c…:toolu_01…:end"
}
```

## Kinds

| kind | main `data` fields |
|------|--------------------|
| `session.start` / `session.end` | `model`, `source` / `reason`, `status` |
| `turn.start` / `turn.end` | `prompt` (by capture level) / `status` |
| `tool.start` / `tool.end` | `tool_call_id`, `tool` (edit, write, read, shell, search, web, mcp, task, other), `ok`, `duration_ms` |
| `shell.exec` | `command`, `exit_code`, `duration_ms` |
| `file.read` / `file.edit` | `path`, `op`, `lines_added`, `lines_removed` |
| `mcp.call` | `server`, `tool`, `ok` |
| `waiting.start` / `waiting.end` | `reason` / `resolution` |
| `subagent.start` / `subagent.end` | `child_session_id`, `agent_type`, `status` |
| `usage` | `model`, `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_write_tokens`, `cost_usd` |
| `compact` | `phase`, `trigger` |
| `git.commit` / `git.branch` | `sha`, `message`, `files` / `from`, `to` |
| `error`, `note` | `message` |

## Rules

- Pair `tool.start`/`tool.end` by `tool_call_id`, never by order (agents run tools in parallel).
- Use `dedup_key` so the same event from a hook and a transcript merges into one.
- Unknown native fields are ignored. Unknown native events are kept as raw, never treated as errors.
