# Sending events to Shiplino

Agents without a Shiplino adapter can still show up on the board. The daemon accepts events on its local API, at the same address as the board (`http://localhost:4777` by default; `shiplino doctor` prints the real one).

- `POST /api/v1/ingest`: universal events, for custom agents and scripts
- `POST /v1/logs` (plus `/v1/metrics` and `/v1/traces`): an OTLP/HTTP receiver for agents that export OpenTelemetry

Both need the local API token. It's in `~/.shiplino/token` (`%USERPROFILE%\.shiplino\token` on Windows) and goes in an `Authorization: Bearer <token>` header. The API only listens on 127.0.0.1. Events take the same path as hook events: secrets are redacted and the capture level applies before anything is stored. While recording is paused, nothing is stored.

## Custom agents: `/api/v1/ingest`

Send a JSON array of [universal events](event-format.md), or NDJSON (one event per line) with `Content-Type: application/x-ndjson`. A request can carry up to 1,000 events and 4 MB (after `Content-Encoding: gzip`, if used).

```bash
curl -s http://localhost:4777/api/v1/ingest \
  -H "Authorization: Bearer $(cat ~/.shiplino/token)" \
  -H "Content-Type: application/json" \
  -d '[
    {"id": "run-42-start", "kind": "turn.start", "agent": {"name": "release-bot"},
     "session_id": "release-bot:run-42", "project": {"cwd": "/home/dev/app"},
     "data": {"prompt": "Cut the 1.4 release"}},
    {"id": "run-42-usage-1", "kind": "usage", "agent": {"name": "release-bot"},
     "session_id": "release-bot:run-42",
     "data": {"model": "claude-sonnet-5", "input_tokens": 1200, "output_tokens": 300, "cost_usd": 0.0081}},
    {"id": "run-42-end", "kind": "turn.end", "agent": {"name": "release-bot"},
     "session_id": "release-bot:run-42", "data": {"status": "ok"}}
  ]'
```

```json
{"accepted": 3, "duplicates": 0, "errors": []}
```

Rules:
- **Required:** `kind`, `agent.name` and `session_id`. The agent name is yours to choose (up to 64 characters, no `:`, `/` or spaces). The session id must be namespaced by it, as `<agent>:<id>`. `actor_id` and `parent_actor`, if set, must be the session id or start with `<session_id>/` (for subagents, e.g. `release-bot:run-42/sub:review`).
- **Filled in for you:** `v` (1), `ts` (now), `received_at` and `collector` (`http`). Shiplino assigns its own event ids.
- **Retries are safe.** Each event's `dedup_key`, or else its `id`, identifies it within its session. Sending the same event again counts as a duplicate. An event with neither is always stored.
- **Errors are per event.** Invalid events are listed by their position (`{"index": 2, "error": "…"}`) and the valid ones are still stored. The status is 400 only when nothing in the request was valid. Larger requests get 413.
- **Cost:** a `usage` event's `cost_usd` is kept as sent. A `usage` event with a `model` and tokens but no `cost_usd` is priced from the bundled price table (`cost_source: computed`), or marked `unpriced` for models the table doesn't know. To show your agent's own figure as the session cost (`cost_source: reported`), also send its running total as `{"report": true, "process": "<any id for this run>", "total_cost_usd": …}` (see [cost.md](cost.md)).

### SDKs

The [TypeScript](../sdk/ts/) and [Python](../sdk/python/) SDKs wrap this endpoint. They find the token and port in the Shiplino home, batch events in the background (every second or 100 events, and at exit), give every event a stable id so retries are safe, keep a bounded queue while the daemon is down, and never throw into your agent.

```python
from shiplino import Shiplino

shiplino = Shiplino(agent="release-bot")
with shiplino.session(title="Cut the 1.4 release") as s:
    s.turn("Cut the 1.4 release")
    with s.tool("Bash", {"command": "make release"}):
        ...
    s.usage("claude-sonnet-5", input_tokens=1200, output_tokens=300, cost_usd=0.0081)
```

```ts
import { Shiplino } from "@shiplino/sdk";

const shiplino = new Shiplino({ agent: "release-bot" });
const s = shiplino.session({ title: "Cut the 1.4 release" });
s.turn("Cut the 1.4 release");
s.tool("Bash", { command: "make release" }).end(true);
s.usage({ model: "claude-sonnet-5", inputTokens: 1200, outputTokens: 300, costUsd: 0.0081 });
s.end();
```

A usage call with a cost sends both the per-response cost and the running total described above, so the card shows your agent's own figure.

## OpenTelemetry: `/v1/logs`

Point the agent's OTLP/HTTP exporter at the daemon. Both `http/protobuf` and `http/json` work. gRPC doesn't.

### Claude Code

```bash
export CLAUDE_CODE_ENABLE_TELEMETRY=1
export OTEL_LOGS_EXPORTER=otlp
export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4777
export OTEL_EXPORTER_OTLP_HEADERS="Authorization=Bearer $(cat ~/.shiplino/token)"
```

The exporter appends `/v1/logs` itself. If you already send telemetry elsewhere, use `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT=http://localhost:4777/v1/logs` with `OTEL_EXPORTER_OTLP_LOGS_HEADERS`, so only the log events come to Shiplino.

What Shiplino records (per Claude Code's monitoring docs, checked 2026-10-10):

| Claude Code event | Becomes |
|-------------------|---------|
| `claude_code.api_request` | `usage`: model, tokens, `cost_usd`, `duration_ms`, `request_id` |
| `claude_code.user_prompt` | `turn.start` (the prompt itself only if you set `OTEL_LOG_USER_PROMPTS=1`) |
| `claude_code.tool_result` | `tool.start` + `tool.end`: tool, success, duration |
| other `claude_code.*` events | counted, not stored |

Metrics (`OTEL_METRICS_EXPORTER=otlp`) are accepted and counted but not stored: the log events carry the same numbers per request. Traces are accepted and counted. Records Shiplino can't map, and records without a `session.id`, are counted as unknown. `shiplino doctor` shows the counts.

### How telemetry and transcripts fit together

With Shiplino's hooks installed, Claude Code's transcripts already give per-response tokens and Claude Code's own running cost total. Telemetry describes the same API calls, so Shiplino never adds the two:

- **Cost:** each `api_request` carries the cost Claude Code computed itself, background calls included. Their sum is kept apart as the session's `telemetry.cost_usd`. Like the running total in the transcript, it's the agent's own figure (`cost_source: reported`), and it's shown when it's at least the cost computed from transcript tokens. When both agent figures exist, the larger is shown, never their sum.
- **Tokens:** transcript (or hook) usage is used whenever a session has any. Telemetry tokens fill in only for sessions without it (`tokens_source: telemetry`), and they're replaced as soon as direct usage arrives.
- **Activity:** tool events use the same dedup keys as the hooks (`tool_use_id`), so a tool call is stored once whichever arrives first. Turns and tools from telemetry only fill sessions that hooks and transcripts don't cover. A telemetry-only session has no turn-end event, so it goes idle after a quiet period instead of moving to Done.

Telemetry is most useful for sessions Shiplino can't otherwise see: Claude Code running on another machine (forwarded by your own collector), or a machine without hooks installed.

### Codex

Codex can export OpenTelemetry logs too (`[otel]` in `~/.codex/config.toml`, with an `otlp-http` exporter pointing at `http://localhost:4777/v1/logs`, `protocol = "binary"` and an `Authorization` header). Shiplino accepts these records but doesn't store them yet: Codex's rollout files and hooks already give per-response tokens (keyed by response id) and every tool call, and Codex's telemetry events don't carry those ids, so storing them would count the same calls twice. They're counted as "not mapped" in `shiplino doctor`.

### Other OpenTelemetry sources

Any OTLP/HTTP exporter can send to `/v1/logs`. Records from sources Shiplino has no mapping for are counted as unknown and dropped. To record a custom agent, send universal events to `/api/v1/ingest` instead.
