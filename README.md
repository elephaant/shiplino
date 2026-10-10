# Shiplino

**The flight recorder and kanban board for your AI coding agents.**
Every project, every session, every subagent, every command, every file, every dollar, across Claude Code, Codex, Cursor and more.

**One command to install. Zero tokens to run.**

> ⚠️ **Status: alpha.** Claude Code, Codex (CLI and desktop app), Cursor (IDE agent and CLI), Gemini CLI, GitHub Copilot CLI, Windsurf, OpenCode and Cline are supported, plus Aider or any CLI agent through `shiplino wrap`, and custom agents through the SDKs. Expect rough edges and report them in Issues.

---

## Why

You run several agents at once (Claude Code in one terminal, Codex in another, Cursor in the editor), often across several projects, each spawning subagents.
Afterwards it's hard to answer simple questions:

- What is each agent doing **right now**? Is one waiting for my approval?
- What did the agents do yesterday, in which project, and which files did they change?
- How long did it take, and how much did it cost?
- Which commit or PR came from which session?

Shiplino answers all of these in one live board, without changing how you work.

## How it works (in one paragraph)

Modern coding agents can run a small program at lifecycle moments (hooks).
`shiplino setup` registers one tiny program in each agent's hook config. It appends one line to a local file and exits in a few milliseconds, **printing nothing**, so the model never sees it and **no tokens are used**.
A local background service merges those events with the agents' own transcript files (exact token counts) and git (commits). It serves a live board at `http://localhost:4777`.
Read more: [How it works](docs/how-it-works.md).

## Features

- **Live board per project:** every repo becomes a project automatically (git remote, worktrees, folders). Each has its own kanban, Running → Waiting on you → Review → Done, with automatic weekly sprints, and there's an "All projects" overview with a "Needs you" queue.
- **Agents and subagents:** many sessions at once, subagents nested under their parent, live "now doing", and idle detection.
- **Session detail:** a timeline of every prompt, tool call, command, file edit and subagent, plus per-file changes (diffs at the `full` capture level) and per-response token usage.
- **Conversation, export and handoff:** a session's prompts, replies and tool calls, read on demand from the agent's own transcript on your machine (never copied into Shiplino's database or synced), with secrets redacted. Export it as Markdown, or copy a short handoff prompt (goal, files changed, open todos, failing commands, last reply) to continue in another agent.
- **Timeline:** a live Gantt of sessions and subagents (running, waiting on you, idle).
- **Cost and tokens** per agent, model, project and day. The agent's own figures are used when it reports them, otherwise list prices, and the source of every number is shown.
- **Insights:** spend, agent working time, time spent waiting on you, and code changed, compared with the previous period.
- **Search** across all history (⌘K), and CSV/JSON export.
- **Notifications** when an agent waits on you (and why: a permission prompt, a question, or your turn), finishes a long turn or fails: on the desktop, and opt-in on your phone or in a team channel via ntfy, a signed webhook, Slack or Discord. Phone and team alerts carry metadata only, never prompts or code ([docs/notifications.md](docs/notifications.md)).
- **Commits linked** to the sessions that made them.
- **History backfill** from agents' own transcripts, so the board isn't empty on day one.
- **Local-first and private:** secrets are redacted before anything is stored, with three capture levels. Nothing leaves your machine unless you turn on [cloud sync](docs/sync-protocol.md), and even then only metadata: never prompts, replies, commands, code or diffs.
- **Budgets and alerts:** daily, monthly and per-project spend limits with alerts at 80% and 100%, and a daily digest.
- **GitHub pull requests** (opt-in): PR state, CI checks and reviews on cards; a merged PR moves its card to Done.
- **Custom agents:** TypeScript and Python SDKs, an HTTP ingest API and an OpenTelemetry receiver ([docs/ingest.md](docs/ingest.md)).

## Planned

- Integrations: Linear and Jira
- VS Code / Cursor extension, an optional read-only MCP server, auto-update
- **Virtual office:** pixel characters that show what each agent is doing

## Supported agents

| Agent | How |
|-------|-----|
| Claude Code, OpenAI Codex, Cursor, Gemini CLI | hooks + transcripts |
| Windsurf (Cascade), editor and JetBrains plugin | hooks: prompts, turns, file reads and edits, commands, MCP calls. Windsurf's hooks carry no token counts, so no cost yet |
| GitHub Copilot CLI | hooks (`~/.copilot/hooks/shiplino.json`; no token usage yet) |
| OpenCode | plugin (`~/.config/opencode/plugins/shiplino.js`, observe-only): sessions, subagents, prompts, tools, file edits, permission prompts, tokens and OpenCode's own cost |
| Cline (VS Code/JetBrains extension and CLI) | hook scripts in `~/Documents/Cline/Hooks`: tasks, prompts, tool calls with their real durations, file reads and edits, commands with exit codes (CLI), MCP calls, subagents. Hooks carry no token counts, so no cost yet |
| Aider, any CLI agent | `shiplino wrap -- <command>` + git (Aider: prompts, tokens, cost and edits from its chat history) |
| Custom agents (Agent SDK, LangGraph, …) | [TypeScript and Python SDKs](sdk/), the [ingest API](docs/ingest.md) or OpenTelemetry |

Want another agent? Open an issue, or read [Adding an agent adapter](docs/adding-an-adapter.md).

## Install

macOS and Linux:

```bash
curl -fsSL https://raw.githubusercontent.com/elephaant/shiplino/main/scripts/install.sh | sh
```

Windows (PowerShell):

```powershell
irm https://raw.githubusercontent.com/elephaant/shiplino/main/scripts/install.ps1 | iex
```

The installer downloads the release for your OS and CPU, **verifies its SHA-256 checksum** (and, if [cosign](https://docs.sigstore.dev/cosign/system_config/installation/) is installed, the Sigstore signature that ties the release to this repository's build), installs to `~/.shiplino/bin`, then runs `shiplino setup`: agents are detected and connected, the daemon starts at login, and the board is at http://localhost:4777.

To see exactly what setup would change in each agent's config first, run `shiplino setup --dry-run`: it prints a unified diff per file and writes nothing. The Settings → Agents page shows the same diff before you connect or remove an agent. Every file Shiplino edits is backed up to `~/.shiplino/backups/` first.

Prefer to run the daemon yourself (a terminal, tmux, your own process manager)? `shiplino setup --no-service` connects the agents without registering a login item; then start `shiplino daemon` when you want recording. Until it runs, hook events wait in `~/.shiplino/spool/`.

Remove everything with `shiplino uninstall` (add `--purge` to delete recorded data, or `--dry-run` to see what would change).

## Build from source

```bash
make build        # builds the web app (if Node is installed) and ./bin/shiplino
./bin/shiplino setup
```

## Repository layout

```
cmd/shiplino/      single binary: CLI, hook shim, daemon
pkg/                shared Go packages: event model, agent adapters, engine, projects/sprints, redaction, pricing
internal/           local-only code: shim, spool, daemon, OS service, SQLite store, git watcher, API, notifications, integrations, sync client
web/                React app (packages/ui = shared components, apps/local = app embedded in the binary)
schema/             JSON Schema of the universal event format
sdk/                TypeScript and Python SDKs for custom agents
plugins/            Claude Code plugin, OpenCode plugin, VS Code extension (planned)
assets/office/      virtual office maps and sprites (planned)
scripts/            install scripts
testdata/, bench/   end-to-end fixtures and benchmarks
docs/               user and contributor docs
```

## Development

Requirements: Go 1.26+ and Node 20+ (for the web app).

```bash
make build     # builds the web app and ./bin/shiplino
make test      # runs tests
make lint      # gofmt + go vet
make bench     # full load test
```

## Commands

```
shiplino setup | uninstall [--purge]   connect agents, install the background service
shiplino setup --dry-run               show the diff of every agent config setup would change; write nothing
shiplino setup --no-service            connect agents only; you run `shiplino daemon` yourself
shiplino uninstall --dry-run           show what uninstall would remove; write nothing
shiplino status | ls | open | doctor   what's running, recent sessions, the board, health checks
shiplino search <words> | export       find anything; sessions as CSV or JSON
shiplino export --session <id>         one conversation as Markdown
shiplino backfill [--since 30d]        import history from before setup
shiplino pause [--for 1h] | resume     stop and restart recording
shiplino wrap -- <command>             record a CLI agent that has no hooks
shiplino sync login | status | allow   opt-in team sync
shiplino notify test | add | list      desktop notifications and phone/team alerts
```

## Contributing

Contributions are welcome, especially **new agent adapters**. Please read [CONTRIBUTING.md](CONTRIBUTING.md) first.
All commits must be signed off ([DCO](https://developercertificate.org/)): `git commit -s`.

- Report a security issue: [SECURITY.md](SECURITY.md) (please don't open a public issue)
- Code of conduct: [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)
- How decisions are made: [GOVERNANCE.md](GOVERNANCE.md)

## Acknowledgments

The web app's layout and its default color theme are inspired by [next-shadcn-admin-dashboard](https://github.com/arhamkhnz/next-shadcn-admin-dashboard) by [Mohammed Arham Khan](https://github.com/arhamkhnz) (MIT). Thank you! Shiplino's screens are our own code, built with [shadcn/ui](https://ui.shadcn.com).

## License

Shiplino is licensed under the [Apache License 2.0](LICENSE).
"Shiplino" and the Shiplino logo are trademarks. See [TRADEMARKS.md](TRADEMARKS.md).
