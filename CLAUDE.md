# Shiplino (open source)

Shiplino is a flight recorder and kanban board for AI coding agents. It records every session, subagent, tool call, shell command, file edit, commit and token cost across Claude Code, Codex, Cursor and more. It shows them live at `http://localhost:4777`.

**This repository is public (Apache-2.0; see [LICENSE](LICENSE)).** Everything you write here, including this file and `.claude/`, will be published. Read [.claude/rules/public-repo.md](.claude/rules/public-repo.md) before adding docs or comments.

Status: alpha (v0.1.0-alpha.3). Claude Code, Codex, Cursor, Gemini CLI, Copilot CLI, Windsurf, OpenCode, Cline and `wrap` agents are supported; the web app, search, notifications (desktop, phone, webhooks), insights, plan limits, timeline, a local conversation view and the opt-in, metadata-only sync client are built.

## How it works

```
agent hook → `shiplino hook` (tiny, prints nothing, exit 0) → ~/.shiplino/spool/<agent>/<session>.jsonl
daemon: spool + agent transcripts (tokens) + git (commits) → adapters → universal events
      → dedup/merge → task engine → one batched SQLite writer + FTS5 (files parsed in parallel)
      → REST + WebSocket on 127.0.0.1:4777 → web UI embedded in the binary
```

More detail: [docs/how-it-works.md](docs/how-it-works.md), [docs/event-format.md](docs/event-format.md), [docs/adding-an-adapter.md](docs/adding-an-adapter.md).

## Layout

| Path | What |
|------|------|
| `cmd/shiplino/` | The single binary: CLI, hook shim, daemon. The `hook` fast path runs before any other init |
| `pkg/` | Public Go API, also used by other Shiplino services: `model`, `adapters/<agent>`, `engine`, `projects`, `redact`, `pricing` |
| `internal/` | Local only: `shim`, `spool`, `daemon`, `service`, `store`, `gitwatch`, `api`, `notify`, `integrations`, `sync` (opt-in sync client), `cli` |
| `web/` | npm workspaces: `packages/ui` (`@shiplino/ui`), `apps/local` (embedded app) |
| `schema/event.v1.json` | JSON Schema of the universal event |
| `sdk/ts`, `sdk/python` | SDKs for custom agents |
| `plugins/` | Claude Code plugin, OpenCode plugin, VS Code extension |
| `testdata/e2e`, `bench/` | Recorded sessions and benchmarks |

Rule: code that other services need goes in `pkg/`. `internal/` can't be imported from another module.

## Commands

```bash
make build   # ./bin/shiplino
make test    # go test ./...
make lint    # gofmt check + go vet
echo '{}' | ./bin/shiplino hook --agent claude-code; echo "exit=$?"   # must print only exit=0
```

Go version comes from `go.mod`. Node 20+ for `web/` once it exists.

## Hard rules

1. **Zero-token contract**: [.claude/rules/zero-token-contract.md](.claude/rules/zero-token-contract.md). The hook never prints, always exits 0, makes no network calls, and stays tiny.
2. **Observe, never control.** Never block, change or add context to an agent.
3. **Privacy**: [.claude/rules/privacy-security.md](.claude/rules/privacy-security.md). Redact before storing, bind to 127.0.0.1 only, and nothing leaves the machine without opt-in.
4. **Agent config edits**: use a real parser, back up first, write atomically, use user-level files only, and never touch a file that fails to parse.
5. **Public repo hygiene**: [.claude/rules/public-repo.md](.claude/rules/public-repo.md).

Path-scoped rules: [go-code.md](.claude/rules/go-code.md), [adapters.md](.claude/rules/adapters.md), [web-ui.md](.claude/rules/web-ui.md), [testing.md](.claude/rules/testing.md).

## UI

A shadcn/ui dashboard with Shiplino's own layout and the single **default theme** (orange primary on cool grey, navy in dark mode), defined in `web/packages/ui/src/styles/theme.css`. The project board is a `@dnd-kit/react` kanban. Stack: Next.js 16 (static export), React 19, Tailwind v4, shadcn/ui, `@dnd-kit/react`, Zustand, Recharts. See [.claude/rules/web-ui.md](.claude/rules/web-ui.md).

## Skills in this repo

- `add-adapter`: add or update support for an agent (detect, install, parse, transcripts, fixtures)
- `zero-token-check`: verify the hook shim contract
- `shiplino-ui`: build a UI screen with shadcn/ui and the default theme
- `kanban-board`: Shiplino's live, auto-moving kanban board

## Claude Code setup for contributors

[.claude/settings.json](.claude/settings.json) suggests the **Ponytail** plugin (keeps code small, reuses the stdlib first). For UI work, also run `npx skills add shadcn/ui`. Don't commit plugins that send session data to third parties. Put personal plugins in `.claude/settings.local.json` (gitignored).

Commits need DCO sign-off: `git commit -s`.
