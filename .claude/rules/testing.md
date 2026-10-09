---
paths:
  - "**/*_test.go"
  - "**/testdata/**"
  - "bench/**"
  - "testdata/**"
  - ".github/workflows/**"
---

# Testing

Agents change their hooks and transcript formats often. Tests exist to catch silent breakage before users do.

| Layer | What | Where |
|-------|------|-------|
| Unit | parsers, normalizer, redaction, state machine, pricing | next to code |
| Golden | real (redacted) hook payloads + transcripts per agent version → expected events JSON | `pkg/adapters/<agent>/testdata/<version>/` |
| Installer | install/uninstall/idempotency on empty, existing, commented, malformed configs | `pkg/adapters/*/install_test.go` |
| Zero-token contract | every event of every adapter through the real shim: empty stdout/stderr, exit 0, even with garbage input, full disk or missing HOME | `internal/shim/contract_test.go` |
| Concurrency | many processes appending to one session file; shuffled event order gives the same final state; 50 sessions × 4 subagents load | `internal/…`, `bench/` |
| Performance | shim p99 < 8 ms, ingest > 20k events/s, board query < 30 ms | `bench/` |
| E2E replay | recorded multi-agent sessions → shim → daemon → API → assert board state | `testdata/e2e/` |
| UI | Vitest components, Playwright for board/drag/search | `web/` |

Rules:
- Update golden files deliberately (`-update` flag). Review the diff and never regenerate blindly.
- Fixtures must contain no real secrets, prompts or usernames.
- Tests are hermetic: use a temp HOME (`t.Setenv("HOME", t.TempDir())`) and never touch the real `~/.claude` etc.
- CI runs on macOS, Linux and Windows.
