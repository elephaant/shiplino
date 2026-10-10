---
paths:
  - "**/*.go"
  - "go.mod"
  - "go.sum"
---

# Go code

- **One binary.** CLI, shim, daemon and the embedded UI all live in `cmd/shiplino`. Don't add new binaries.
- **`pkg/` vs `internal/`:** anything other Shiplino services reuse (event model, adapters, engine, projects, redact, pricing) goes in `pkg/` and is treated as a semver'd public API. Everything else goes in `internal/`.
- **Minimal dependencies.** Prefer the stdlib. Planned and approved: `modernc.org/sqlite` (pure Go, no CGO), `fsnotify`, `coder/websocket`, `pelletier/go-toml/v2`, `tidwall/gjson`/`sjson`, `zalando/go-keyring`, OTel `pdata`. Ask before adding others, and never add one to `internal/shim`.
- **No CGO.** Builds must cross-compile for macOS, Linux and Windows (amd64 + arm64).
- **SQLite:** WAL mode, a single writer goroutine, batched transactions (up to 500 events, across all files read in a pass). Advance source offsets (`cursors`) in the same transaction as the events they produced. Checkpoints run beside the writer, never inside a commit.
- **Concurrency:** read, parse and redact source files in parallel, each file by one goroutine at a time (a file is one session, so its order holds). The engine and the writer stay on one goroutine: they're a small share of the cost. Measure with `make bench` before adding sharding or locks.
- **Tolerant parsing:** ignore unknown fields, and keep unknown event types as `raw` with a counter. Never fail on agent format drift.
- **Errors:** wrap with `%w` and context. The daemon logs to `~/.shiplino/logs/daemon.log` and never to stdout when it runs as a service.
- **Cross-platform paths:** use `filepath` and `os.UserHomeDir()`. Remember that Windows uses `%USERPROFILE%\.shiplino`.
- **Style:** `gofmt`, `go vet`, table-driven tests, small packages. No per-file license headers.
- Bind network listeners to `127.0.0.1` only.
