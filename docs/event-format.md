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
| `tool.start` / `tool.end` | `tool_call_id`, `tool` (edit, write, read, shell, search, web, mcp, task, other), `ok`, `duration_ms`; `tool.end` may add `denied` (the call wasn't allowed to run) and `interrupted` |
| `shell.exec` | `command`, `exit_code`, `duration_ms`, `program` (the command's program name, e.g. `go`, `npm`: at most 32 characters of `[a-z0-9._-]`, derived by the daemon from the redacted command and kept at every capture level) |
| `file.read` / `file.edit` | `path`, `op`, `lines_added`, `lines_removed`, `lines_source`; edits also carry `patch` (unified-diff hunks, capture level full only, at most 64 KB, `patch_truncated` when cut), `patch_source` (`agent`: the agent's own diff, `computed`: built from the edit's old and new text) and `patch_omitted` when the diff was dropped (`capture_level` below full, `secret_file` for files whose contents are never stored) |
| `mcp.call` | `server`, `tool`, `ok` |
| `waiting.start` / `waiting.end` | `reason` / `resolution`, `denied` when the user refused a permission |
| `subagent.start` / `subagent.end` | `child_session_id`, `agent_type`, `status` |
| `usage` | per response: `model`, `message_id`, `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_write_tokens`, `web_searches`, `cost_usd`, `cost_source` (`computed` / `unpriced`, or `reported` when the agent priced the response itself); agent cost report: `report: true`, `process`, `total_cost_usd`, `cost_source: reported` (see [cost.md](cost.md)) |
| `compact` | `phase`, `trigger` |
| `git.commit` / `git.branch` | `sha`, `message`, `files` / `from`, `to`, `action` |
| `git.push` / `git.pr` | `branch` / `number`, `url`, `action` (as reported by the agent) |
| `session.update` | metadata the agent reports, e.g. `title` with `title_source: agent`; or the agent's own todo list: `plan_items` (`[{id, text, status}]`, status `pending`, `in_progress`, `completed`, `cancelled` or `blocked`; `text` only at capture level standard and full), `plan_merge: true` when the items update the list by id (`deleted` removes one) instead of replacing it, and `plan_total` (items not cancelled) and `plan_done` (completed). The daemon adds the counts to a merge before storing it. Only the counts are synced |
| `limit` | a plan usage window as the agent reports it: `limit_window` (`5h`, `7d`, …), `window_minutes`, `used_percent`, `limit_reached`, `resets_at`, `limit_id`, `plan_type`, `limit_source: reported` (see [cost.md](cost.md#plan-limits)) |
| `error`, `note` | `message` |

## Rules

- Pair `tool.start`/`tool.end` by `tool_call_id`, never by order (agents run tools in parallel).
- Use `dedup_key` so the same event from a hook and a transcript merges into one.
- Count usage once per model response: some agents write one response across several transcript lines that repeat the same usage (key usage events by the response id).
- Unknown native fields are ignored. Unknown native events are kept as raw, never treated as errors.
