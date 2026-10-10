# Shiplino

**The flight recorder and kanban board for your AI coding agents.**
Every project, every session, every subagent, every command, every file, every dollar, across Claude Code, Codex, Cursor and more.

**One command to install. Zero tokens to run.**

> ⚠️ **Status: alpha.** Claude Code, Codex (CLI and desktop app) and Cursor (IDE agent and CLI) are supported today. Expect rough edges and report them in Issues.

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

## Planned features

- **Projects:** every repo becomes a project automatically, each with its **own board** and **automatic weekly sprints**. Many projects run at the same time, with an "All projects" overview.
- **Kanban that moves itself:** Running → Waiting on you → Review → Done. Cards appear and move based on what agents actually do.
- **Parallel agents and subagents:** each tracked live, nested under their parent, with cost and time rolled up.
- **Timeline** of every prompt, tool call, command and file edit, plus **search** across all history.
- **Cost and token tracking** per agent, model, project and sprint.
- **Virtual office:** pixel characters that show what each agent is doing.
- **Integrations:** GitHub PRs, Linear, Jira, Slack/Discord, webhooks, OpenTelemetry.
- **Local-first and private:** nothing leaves your machine unless you turn on team sync.

## Supported agents (planned)

| Agent | How |
|-------|-----|
| Claude Code, OpenAI Codex, Cursor, Gemini CLI | hooks + transcripts |
| Windsurf (Cascade) | hooks: prompts, turns, file reads and edits, commands, MCP calls. Windsurf's hooks carry no token counts, so no cost yet |
| GitHub Copilot CLI | hooks (`~/.copilot/hooks/shiplino.json`; no token usage yet) |
| Cline, OpenCode | hooks / plugins |
| Aider, any CLI agent | logs, git, `shiplino wrap` |
| Custom agents (Agent SDK, LangGraph, …) | HTTP, OTLP, SDKs |

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

Remove everything with `shiplino uninstall` (add `--purge` to delete recorded data).

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
plugins/            Claude Code plugin, OpenCode plugin, VS Code extension
assets/office/      virtual office maps and sprites
scripts/            install scripts
testdata/, bench/   end-to-end fixtures and benchmarks
docs/               user and contributor docs
```

## Development

Requirements: Go 1.26+ (Node 20+ once the web app lands).

```bash
make build     # builds ./bin/shiplino
make test      # runs tests
make lint      # gofmt + go vet
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

The code in this repository is licensed under the [Functional Source License (FSL-1.1-Apache-2.0)](LICENSE).
It is free to use, modify, and self-host for internal development and business use, with a restriction prohibiting competitors from offering it as a competing commercial product or service. Each release converts to Apache-2.0 after two years.
"Shiplino" and the Shiplino logo are trademarks. See [TRADEMARKS.md](TRADEMARKS.md).
