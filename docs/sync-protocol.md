# Sync protocol

Shiplino is local-first. Sync is the one feature that sends your recordings to a server, so it's opt-in and the client is open source: this page describes exactly what it sends and how to check it yourself. The client lives in [`internal/sync`](../internal/sync).

Nothing leaves your machine unless **all** of these are true:

1. You signed in with `shiplino sync login` (that also sets `[sync] enabled = true`).
2. The project is on your allow list (`shiplino sync allow <project>`). **The allow list starts empty, so a fresh sign-in sends nothing.**
3. The daemon is running.

## Commands

```bash
shiplino sync login [--endpoint URL]   # sign this device in (device code + browser)
shiplino sync allow <project>          # a project id, a recorded project's name, a glob, or "." for this folder
shiplino sync deny <project>           # stop syncing it (data already uploaded stays in the workspace)
shiplino sync status                   # workspace, last upload, backlog, last error
shiplino sync status --dry-run         # print the next batch, exactly as it would be sent, without sending it
shiplino sync logout                   # delete the credentials and turn sync off
```

`shiplino doctor` and the Settings page also show the sync state.

## Configuration

`~/.shiplino/config.toml` (the `shiplino sync` commands rewrite this section; the rest of the file is left alone). Changes apply within a few seconds, without restarting the daemon.

```toml
[sync]
enabled = false                         # set by `sync login` / `sync logout`
endpoint = "https://api.shiplino.com"   # optional; https only (plain http works for localhost)
projects = []                           # allow list: project ids or globs; empty = nothing is sent
exclude = []                            # project ids or globs removed from the allow list
send_titles = false                     # also send session titles (redacted, 120 characters)
```

- **Project ids** are the ones the board shows: `github.com/acme/api` for a repo with a remote, `local:/path/to/repo` for a repo without one, `dir:/path` for other folders, and `unsorted`. In globs, `*` matches anything (including `/`) and `?` one character: `github.com/acme/*` allows every repo of that owner, and `*` allows everything, including events with no known project.
- **`send_titles`** also sends session titles. They're off by default because a title can say what the work is about. The service drops titles too unless the workspace allows them.
- Older settings `capture_level` and `send_user` are ignored (sync is metadata only, and the OS user name is never sent); `shiplino sync status` says so.
- When you allow more projects, the client reads your history again from the start, so the new projects' past sessions are uploaded too. The server ignores events it already has.

## What is sent

**Metadata only. Conversations never leave your machine.** Prompts, the agent's replies, shell commands, tool input and output, error text, diffs, commit messages and file contents are never sent, at any setting. Your full history stays in your local Shiplino and in the agents' own files.

Events go in the [universal event format](event-format.md), one JSON object per event. Before each event is sent, the client:

1. drops it unless its project is allowed and not excluded (the project is the event's own, or its session's),
2. makes local paths project-relative (below) and drops the project's local folders (`cwd`, `repo_root`),
3. keeps only the **metadata fields** listed below and drops every other `data` field, including fields added in later versions until they're reviewed,
4. checks values: fields other than paths, the waiting text and a pull request URL must be a short token (letters, digits and `_.:/@+-`, up to 128 characters) or they're dropped, so free text can't travel in a field like `status`; a URL must be a plain `https://host/path` with no query,
5. runs the redaction rules again (built-in rules plus your `[redaction] extra_patterns`) on what's left and on the envelope ids, and caps each string at 512 characters,
6. replaces `dedup_key` with a hash of it (keys can contain a path or a title fingerprint), reduces `project.remote` to `host/owner/repo` (no credentials), and keeps `agent.version`, `agent.surface`, `project.branch` and `project.head` only when they're tokens,
7. removes `raw` (a pointer into local files) and the OS `user` name.

The sync service applies the same filter again when it receives events, so an older or modified client can't make it store more.

| Sent | Fields |
|------|--------|
| Envelope | `id`, `v`, `ts`, `received_at`, `kind`, `agent`, `collector`, `machine_id`, `session_id`, `actor_id`, `parent_actor`, `actor_type`, `turn_id`, `dedup_key`, `project` (`id`, `remote`, `branch`, `head`; no local folders) |
| Tools, agents and models | `tool`, `tool_raw`, `tool_call_id`, `agent_type`, `agent_id`, `attribution`, `child_session_id`, `model`, `speed`, `inference_geo`, `message_id`, `request_id`, `agent_version`, `wrapped`, `card_id` |
| Tokens and cost | `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_write_tokens`, `cache_write_1h_tokens`, `reasoning_tokens`, `web_searches`, `tokens`, `tokens_rounded`, `tokens_source`, `prompt_chars`, `cost_usd`, `cost_source`, `total_cost_usd`, `message_cost_usd`, `report`, `process`, `correction`, `model_usage` (numbers only) |
| Outcomes and timing | `ok`, `exit_code`, `duration_ms`, `status`, `reason`, `signal`, `interrupted`, `stop_reason`, `recoverable`, `permission_mode`, `source`, `trigger`, `denied`, and a shell command's `program` (its program name, e.g. `go`: a checked token of at most 32 characters of `[a-z0-9._-]`, never the command) |
| Files | `path`, `file_path`, `files`, `file_paths`, `paths` (project-relative), `lines_added`, `lines_removed`, `lines_source`, `files_changed`, `patch_omitted`, and a file tool's `input_summary` (its path) |
| Git | `sha`, `branch`, `to`, `number`, `state`, `action`, `head`, and a pull request's `url` |
| Plan progress | `plan_total`, `plan_done` (counts, not the items) |
| Waiting | `message`, replaced by a generic text such as "Waiting for your approval" |
| Titles | `title`, `title_source` of session events, only with `send_titles = true` |

The list is `syncKeys` in [`pkg/redact/sync.go`](../pkg/redact/sync.go).

**Paths.** The data fields `path`, `file_path`, the lists `files`, `file_paths` and `paths`, and a file tool's `input_summary` (each path, when it lists several) are made relative to the event's project root. That root is `project.repo_root`, else the folder in a `local:` or `dir:` project id, else the working directory. For example, `/home/alex/code/api/src/auth.go` becomes `src/auth.go` and the root itself becomes `.`. A path outside the project keeps only its base name with a `…/` prefix: `/home/alex/.ssh/config` becomes `…/config`. Paths that were already relative are left as they are. The project id itself is sent as it is, and for repos without a remote (`local:…`) and plain folders (`dir:…`) that id contains the folder's absolute path.

The sign-in request carries the hostname as `device_name`. Each upload also carries a `device_id` (a random id created once and kept in `~/.shiplino/device_id`) and a `device_name` (the computer's hostname).

### Never sent

- Prompts, the agent's replies and summaries, shell commands, tool input and output, error text, diffs, commit messages, notification text, todo item text and file contents.
- Session titles, unless you set `send_titles = true` and the workspace allows them.
- Your OS user name, git author names and absolute local paths other than the project id.
- Anything from a project that isn't allowed, or that's excluded.
- Anything while sync is off, signed out or paused by an error that needs you to sign in again.
- Raw hook payloads and transcripts, and the `raw` pointer to them.
- Your local API token, the Settings page cookie, and the sync tokens themselves (except as the `Authorization` header to the sync endpoint).

## How to audit it

- `shiplino sync status --dry-run` prints the next batch exactly as the client would send it (after filtering and redaction) and sends nothing.
- The code that builds each event is `Scope.Outgoing` and `Scope.Prepare` in [`internal/sync/scope.go`](../internal/sync/scope.go); the HTTP calls are all in [`internal/sync/protocol.go`](../internal/sync/protocol.go).
- Point the client at your own server with `shiplino sync login --endpoint http://localhost:8080` and log what arrives.

## Credentials

The access and refresh tokens are stored in the OS keychain: Keychain on macOS, Credential Manager on Windows, and the Secret Service (GNOME Keyring, KWallet) on Linux. When there's no keychain, as on a headless Linux server, they go in `~/.shiplino/sync-credentials.json`, readable only by you (mode `0600`). That's the same trade-off `gh` and `docker` make. `shiplino doctor`, `shiplino sync status` and `sync login` say which one is used. The tokens never appear in `config.toml`, logs, the local API or the Settings page.

## Wire protocol (v1)

All paths are relative to the endpoint. Requests and responses are JSON. Errors look like `{"error": "<code>"}`.

### Sign-in: device authorization (RFC 8628)

`POST /v1/device/code` with `{"device_name": "<hostname>"}` (optional; shown on the approval page) → `200`

```json
{"device_code": "…", "user_code": "ABCD-EFGH", "verification_uri": "https://…", "verification_uri_complete": "https://…?code=ABCD-EFGH", "interval": 5, "expires_in": 900}
```

The CLI shows `user_code` and `verification_uri`, opens `verification_uri_complete` in the browser, then polls every `interval` seconds:

`POST /v1/device/token` with `{"device_code": "…"}` →

| Answer | Meaning | Client |
|--------|---------|--------|
| `428 {"error":"authorization_pending"}` | not approved yet | keep polling |
| `429 {"error":"slow_down"}` | polling too fast | add 5 seconds to the interval, or wait for `Retry-After` if that's longer |
| `400 {"error":"expired_token"}` | the code expired (or is unknown or already used) | stop, ask to run `login` again |
| `400 {"error":"invalid_request"}` | the request had no code | stop |
| `400 {"error":"access_denied"}` | the user declined | stop |
| `200` | approved | store the tokens |

```json
{"access_token": "…", "refresh_token": "…", "expires_in": 3600, "workspace_id": "…", "workspace_name": "…"}
```

### Refresh

`POST /v1/token/refresh` with `{"refresh_token": "…"}` → the same `200` shape, or `400 {"error":"invalid_grant", "message": "…"}` (RFC 6749) when the refresh token is no longer valid. The client refreshes a minute before `expires_in` runs out, and after any `401`. If the refresh is refused, the client deletes the stored tokens, stops uploading, and `shiplino sync status`, `doctor` and the Settings page say the device was signed out and to run `shiplino sync login`.

### Who am I

`GET /v1/me` with `Authorization: Bearer <access_token>` → `{"user": {…}, "workspace": {…, "role": "…"}, "device": {…}}`. At sign-in the client shows the account's email or name and its workspace role.

Device tokens can only upload events. The client never reads data back from the service.

### Upload events

`POST /v1/sync/events` with `Authorization: Bearer <access_token>`, `Content-Type: application/json`, `Content-Encoding: gzip`:

```json
{"device_id": "01J…", "device_name": "dev-laptop", "events": [ { "id": "01J…", "v": 1, "kind": "tool.end", "…": "…" } ]}
```

- At most **2,000 events** and **8 MB** (uncompressed) per request.
- The server is idempotent on the event `id` and `dedup_key`: sending an event twice stores it once.
- `200 {"accepted": n, "duplicates": m, "rejected": k}`. `rejected` (optional) counts events that failed the server's schema check and were dropped. The client moves past them, never resends them, and shows the count in `shiplino sync status`, `doctor` and the Settings page.

| Answer | Client |
|--------|--------|
| `400` | the request was malformed (a client bug): log it and back off as below, never in a tight loop |
| `401` | refresh the token and retry once; if the refresh fails, sign out as described above |
| `403` | the account's workspace role can't sync: stop uploading, show it in `sync status` and `doctor`, and check again once an hour |
| `413` | split the batch in half and send each half; a single event that's still too large is skipped and logged |
| `429`, `503` | wait for `Retry-After` (seconds or an HTTP date), else back off exponentially with jitter (5 s doubling to 5 min) |
| other errors, offline | back off the same way and retry |

### Ordering, batching and resume

The daemon stores events in SQLite. The sync client runs on its own goroutine and reads committed events only, through a separate read-only connection, so it never slows recording. It uploads them in the order they were stored, one batch every 5 seconds while there's a backlog, and records how far it got per workspace (the last uploaded event's row id) after every accepted request. After a crash or while offline it resumes from there. A batch accepted by the server just before a crash is sent again, and the server drops the duplicates.
