# Privacy and security

Shiplino sees prompts, commands, file paths and sometimes secrets. Trust is the product.

- **Local by default.** Nothing leaves the machine unless the user enables sync or an integration. Adding a new outbound network call requires an explicit opt-in setting.
- **Capture levels:** `minimal` (no prompts, commands or outputs), `standard` (default: truncated prompts, commands, short summaries), `full` (capped tool I/O, raw payloads). Every new captured field must declare which level it belongs to.
- **Conversations never leave the machine.** Sync and every cloud or team feature work on metadata only (names, ids, counts, timings, outcomes, project-relative paths, git refs). A new `data` field is local-only until it's reviewed and added to `syncKeys` in `pkg/redact/sync.go`; content (prompts, replies, commands, tool I/O, errors, diffs, commit or notification text, todo text) is never added. When a feature needs content, it reads it locally, ideally on demand from the agent's own files, instead of copying it. Every feature says which of its fields sync.
- **Redact before storing** (in the daemon, before SQLite) **and again before sync.** Use the `pkg/redact` ruleset (gitleaks-based, known token formats, secret-looking `KEY=value`, high-entropy strings). Replace values with `«redacted:<kind>»`. Never store the contents of `.env*`, `*.pem`, `id_rsa*` or `credentials*` files. Store only the path.
- **Spool holds raw payloads:** `0700` directory, `0600` files, deleted once processed.
- **Local API:** bind `127.0.0.1` only. Require a 256-bit token from `~/.shiplino/token` (`0600`) on every call, including `/ingest` and OTLP. Use an HttpOnly SameSite=Strict cookie for the UI, check the Host header (`localhost`/`127.0.0.1`) against DNS rebinding, keep CORS strict, and check the WebSocket `Origin`.
- **Secrets for integrations** go in the OS keychain (`go-keyring`), never in `config.toml`.
- **Supply chain:** pinned deps, signed releases (cosign), and the installer verifies checksum and signature before running.
- **Logs never contain** prompts, commands, tokens or file contents at any level above debug.

Run `/security-review` for changes to the shim, redaction, API auth, sync or installer.
