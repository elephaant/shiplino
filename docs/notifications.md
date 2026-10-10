# Notifications

Shiplino tells you when an agent is waiting on you, when a long turn finishes and when a session fails. It also alerts on budgets (80% and 100%), plan usage limits and the daily digest. Notifications only watch: they never answer, block or steer an agent.

## Why it's waiting

The board, the "Needs you" queue and every notification say why an agent waits:

| Reason | Shown as | From |
|--------|----------|------|
| `permission` | Needs approval | a permission prompt for a tool or command |
| `question` | Has a question | a question or form for you (e.g. an MCP elicitation) |
| `idle` | Your turn | the agent finished and waits for your next prompt |

A waiting card has a dot until you open it, so you can see which waits are new. That state is kept in your browser only.

On your own machine (the board and desktop notifications), Shiplino also shows the agent's own notification text, such as "Approve: npm run db:migrate". That text never goes to phone or team targets.

## Desktop

On by default. Check it with `shiplino notify test desktop`. Settings are in the `[notify]` section of `~/.shiplino/config.toml`: `waiting`, `finished`, `min_turn` and `failed` choose the events (for push targets too), and `enabled = false` turns desktop notifications off.

## Phone and team targets

Every target is an outbound connection, so none is on until you add it:

```bash
shiplino notify add ntfy                         # phone: makes an unguessable topic to subscribe to
shiplino notify add ntfy --server https://ntfy.example.com --topic alerts --token
shiplino notify add slack                        # paste the incoming webhook URL when asked
shiplino notify add discord                      # paste the channel webhook URL when asked
shiplino notify add webhook https://example.com/hook --sign
shiplino notify test                             # desktop + every target
shiplino notify list                             # targets and their last delivery
shiplino notify remove slack
```

Leave a URL out of the command to paste it at the prompt, so it stays out of your shell history. Webhook URLs, ntfy topics and tokens and the signing secret are credentials: they're kept in the OS keychain (a `0600` file in `~/.shiplino` where there's none, and `notify list` says so), never in `config.toml`. The `[notify.push]` section only lists which targets are on and which events they get:

```toml
[notify.push]
targets = ["ntfy"]
events = []   # any of "waiting", "failed", "done", "budget", "limit", "digest"; empty = all but "done"
```

Changes apply within seconds, without a restart. URLs must use https (plain http only works for localhost).

## What is sent

Metadata only, at every capture level. One function builds what leaves your machine from this list of fields, and a test checks that nothing else can:

| Field | Example |
|-------|---------|
| `event` | `waiting`, `done`, `failed`, `budget`, `limit`, `digest`, `test` |
| `at` | `2026-10-10T09:00:00Z` |
| `session_id`, `agent`, `agent_name` | `claude-code:abc`, `claude-code`, `Claude Code` |
| `project`, `branch` | `api`, `feat/login` (the project's short name) |
| `status`, `reason` | `waiting`, `permission` |
| `duration_ms`, `cost_usd` | session time so far (or the turn, for `done`), cost |
| `url` | a link to the session on your local board, e.g. `http://localhost:4777/session/?id=…` (it opens only on this machine) |
| `scope`, `limit_usd`, `percent` | budgets: `today`, `month` or `project:<name>` |
| `window`, `resets_at` | plan usage windows: `5h`, `7d` |
| `sessions`, `failed`, `files_changed`, `waiting_ms`, `agents` | the daily digest (counts and agent ids) |

Never sent: prompts, session titles, replies, commands, file names or contents, error text, or the agent's notification text.

## Webhook format

`POST` with `Content-Type: application/json`:

```json
{
  "version": 1,
  "source": "shiplino",
  "delivery": "9f2c4a1be07d3e55",
  "sent_at": "2026-10-10T09:00:03Z",
  "alerts": [
    {"event": "waiting", "at": "2026-10-10T09:00:00Z", "session_id": "claude-code:abc", "agent": "claude-code",
     "agent_name": "Claude Code", "project": "api", "branch": "feat/login", "status": "waiting",
     "reason": "permission", "duration_ms": 300000, "cost_usd": 1.23,
     "url": "http://localhost:4777/session/?id=claude-code%3Aabc"}
  ]
}
```

`alerts` has more than one entry when a burst was merged. `X-Shiplino-Delivery` repeats `delivery`; it stays the same across retries, so you can drop duplicates.

With `--sign`, Shiplino makes a secret, shows it once, and signs every request:

```
X-Shiplino-Timestamp: 1791622803
X-Shiplino-Signature: sha256=<hex HMAC-SHA256 of "<timestamp>.<raw body>" keyed with the secret>
```

To verify, compute the same HMAC over the timestamp, a dot and the raw body, compare in constant time, and reject timestamps more than a few minutes old. Run `shiplino notify add webhook <url> --sign` again to rotate the secret.

ntfy, Slack and Discord get a short message rendered from the same fields: a title such as "Claude Code needs your approval", the project and branch, duration and cost, and the board link. Discord messages never ping anyone.

## Delivery

- Asynchronous: each target has its own queue, so a slow or dead target never delays recording or other targets.
- At most one message per target every 10 seconds; alerts that arrive in between are merged into one message.
- 10-second timeout; network errors, 429 and 5xx are retried 3 times with backoff (honoring `Retry-After` up to a minute). Other 4xx errors aren't retried. Redirects aren't followed.
- `shiplino doctor` and `shiplino notify list` show each target's last delivery: when, and the error if it failed (without the URL).
