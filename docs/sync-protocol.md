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
capture_level = "minimal"               # minimal (default), standard or full
projects = []                           # allow list: project ids or globs; empty = nothing is sent
exclude = []                            # project ids or globs removed from the allow list
send_user = false                       # also send your OS user name (standard and full only)
```

- **Project ids** are the ones the board shows: `github.com/acme/api` for a repo with a remote, `local:/path/to/repo` for a repo without one, `dir:/path` for other folders, and `unsorted`. In globs, `*` matches anything (including `/`) and `?` one character: `github.com/acme/*` allows every repo of that owner, and `*` allows everything, including events with no known project.
- **`send_user`** sends the OS user name recorded with each event. It's off by default and never applies at `minimal`.
- **`capture_level`** can't be higher than the local `capture_level`: Shiplino never stored more than that. A higher setting is lowered, and `shiplino sync status` says so.
- When you allow more projects, the client reads your history again from the start, so the new projects' past sessions are uploaded too. The server ignores events it already has.

## What is sent

Events in the [universal event format](event-format.md), one JSON object per event. Before each event is sent, the client:

1. drops it unless its project is allowed and not excluded (the project is the event's own, or its session's),
2. applies the **sync** capture level (the same rules as the local capture levels, below),
3. runs the redaction rules again (built-in rules plus your `[redaction] extra_patterns`), so rules added after an event was recorded still apply,
4. removes `raw` (a pointer into local files) and, unless `send_user = true` (never at `minimal`), the OS `user` name,
5. at `minimal`, makes local paths project-relative (below).

| Capture level | Event fields sent |
|---------------|-------------------|
| `minimal` (default) | `id`, `v`, `ts`, `received_at`, `kind`, `agent`, `collector`, `machine_id`, `session_id`, `actor_id`, `parent_actor`, `actor_type`, `turn_id`, `project` (`id`, `remote`, `branch`, `head`; no `cwd` or `repo_root`), `dedup_key`, and the `data` fields that aren't content: tool names, project-relative file paths, line counts, exit codes, durations, statuses, models, token counts and cost, git SHAs, branches and PR numbers. A waiting event's message becomes a generic "Waiting for your approval". Never `user`. |
| `standard` | Everything `minimal` sends, plus `project.cwd` and `project.repo_root`, file paths as recorded (absolute), and prompts (truncated to 2,000 characters), shell commands, session titles, short tool summaries, the agent's final message (500 characters), tool errors (500 characters) and commit messages, all redacted. `user` only with `send_user = true`. |
| `full` | Everything stored locally at `full`, still redacted. `user` only with `send_user = true`. |

**Paths at `minimal`.** The data fields `path`, `file_path`, `cwd`, `transcript_path`, `files[]` and a file tool's `input_summary` are made relative to the event's project root. That root is `project.repo_root`, else the folder in a `local:` or `dir:` project id, else the working directory. For example, `/home/alex/code/api/src/auth.go` becomes `src/auth.go` and the root itself becomes `.`. A path outside the project keeps only its base name with a `…/` prefix: `/home/alex/.agent/sessions/s1.jsonl` becomes `…/s1.jsonl`. Paths that were already relative are left as they are. The project id itself is sent as it is, and for repos without a remote (`local:…`) and plain folders (`dir:…`) that id contains the folder's absolute path.

The sign-in request carries the hostname as `device_name`. Each upload also carries a `device_id` (a random id created once and kept in `~/.shiplino/device_id`) and a `device_name` (the computer's hostname).

### Never sent

- Anything from a project that isn't allowed, or that's excluded.
- Anything while sync is off, signed out or paused by an error that needs you to sign in again.
- Secrets that the redaction rules recognize (they're replaced by `«redacted:<kind>»` locally and again before sending).
- File contents. Shiplino never stores them, only paths.
- At `minimal`: absolute local paths other than the project id (see above), and your OS user name.
- Raw hook payloads and transcripts, and the `raw` pointer to them.
- Your local API token, the Settings page cookie, and the sync tokens themselves (except as the `Authorization` header to the sync endpoint).

## How to audit it

- `shiplino sync status --dry-run` prints the next batch exactly as the client would send it (after filtering, the capture level and redaction) and sends nothing.
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
