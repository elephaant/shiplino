# `shiplino report`: usage without setup

`shiplino report` reads the transcript files your agents already keep and prints a summary: sessions, active time, tokens and API-equivalent cost per agent, top projects and models, plan limits, and failures. It is a quick way to try Shiplino.

```
shiplino report [--since 7d] [--agent <name>] [--project <name>] [--json]
```

| Flag | Meaning |
|------|---------|
| `--since` | Look-back window: `7d` (default), `12h`, `90m` |
| `--agent` | One agent only, e.g. `claude-code`, `codex`, `cursor` |
| `--project` | One project only, by name (`api`) or id (`github.com/acme/api`) |
| `--json` | Machine-readable output (schema below) |

## What it does and doesn't do

- **Read-only.** It needs no setup. It starts no daemon, installs no hooks or service, and creates no database, config or spool: nothing is written under `~/.shiplino` or anywhere else. The only other program it runs is `git`, read-only, to name projects.
- **Same numbers as the board.** Transcripts are found and parsed by the same adapters the daemon uses, with the same price table ([cost.md](cost.md)) and session engine, all in memory. Files are parsed in parallel. When stderr is a terminal, progress is shown there.
- **Metadata only.** Every event is cut to the `minimal` capture level and redacted before it is counted. The output has names, counts, timings, tokens and costs, never prompts, replies or commands.
- **Transcripts only.** Agents whose transcripts Shiplino can't find, or whose transcripts carry no token counts (Cursor, for example), show sessions without tokens. Hooks see more: after `shiplino setup`, the board also has live state, waiting time and linked commits.

A session is in the window when its top-level session started in it. All of its cost and tokens count, subagents included, as on the Insights page.

## JSON schema (version 1)

`schema` is `1`. Within a version, fields are only added, never renamed or removed. Times are RFC 3339; durations are milliseconds.

| Field | Meaning |
|-------|---------|
| `since`, `until` | The window |
| `agent`, `project` | The filters, if given |
| `transcripts`, `unreadable` | Files read, and files that failed to read |
| `lines`, `bad_lines` | Lines read, and lines that failed to parse or were too long (over 4 MB) |
| `totals` | A usage object (below) for everything |
| `agents[]` | A usage object per agent, plus `agent` and `failures` (failure counts); by cost |
| `projects[]` | `id`, `name`, `sessions`, `active_ms`, `cost_usd`; by cost |
| `models[]` | `model`, `actors` (sessions and subagents with it as their model), `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_write_tokens`, `cost_usd` (computed); by cost |
| `limits[]` | The newest plan usage window per agent: `agent`, `window` (`5h`, `7d`), `limit_id`, `used_percent` (absent when unknown), `limit_reached`, `resets_at`, `plan_type`, `at` (when it was reported) |
| `failures` | Failure counts for everything, plus what failed most: `tools[]` (`agent`, `tool`, `failures`, `denials`) and `shell[]` (`program`, `exit_code`, `failures`) |

A **usage object** has:

| Field | Meaning |
|-------|---------|
| `sessions`, `subagents` | Top-level sessions, and their subagents |
| `turns`, `tool_calls` | Prompts answered; tool calls (subagents' included) |
| `active_ms` | Time spent in turns, not idle time between them |
| `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_write_tokens` | Every response's tokens, subagents included |
| `cost_usd` | API-equivalent cost: the agent's own figure when it reports one, otherwise computed from tokens at list prices |
| `reported_cost_usd`, `computed_cost_usd` | How much of `cost_usd` came from each source |
| `unpriced_sessions` | Sessions with tokens from models without a known price (not in the cost) |
| `no_usage_sessions` | Sessions that did work while the agent recorded no token usage |

**Failure counts** are `tool_failures`, `shell_failures` (non-zero exit codes), `denials`, `retry_loops` and `ended_badly` (sessions whose last turn ended on an error or was interrupted).
