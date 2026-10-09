# Changelog

All notable changes to this project will be documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- OpenAI Codex support. `shiplino setup` adds hooks to `~/.codex/hooks.json` for all 12 Codex hook events (backed up, idempotent, your own hooks kept); Codex asks once to trust them under `/hooks`. Sessions from the Codex desktop app, which run without hooks, are found in `~/.codex/sessions` and read from their rollout files: prompts, turns, commands with exit codes, file changes with Codex's own line counts, MCP calls, model changes and per-response token usage (Codex's `token_usage_record`). When a session has hooks, its activity comes from the hooks and its rollout only adds usage, so nothing is counted twice.
- Cursor support (IDE agent and `cursor-agent`). `shiplino setup` adds observe-only hooks to `~/.cursor/hooks.json`: never permission hooks, never `failClosed`. It records prompts, turns, tool calls with durations, shell commands with exit codes, file edits, MCP calls, subagents (nested under their parent) and compaction. Token usage is recorded when Cursor includes it in `afterAgentResponse`.
- Cursor also runs Claude Code and Codex hooks. Those copies are recognized and skipped, so a Cursor session is never recorded twice.
- `doctor` and `uninstall` cover every connected agent.
- Search: prompts, commands, file paths, session titles and commit messages are indexed (SQLite FTS5, existing data included) and searchable with ⌘K / Ctrl K in the web app, `shiplino search <words>` and `GET /api/v1/search`. Every word must match, as a prefix.
- Export: `shiplino export [--format csv|json] [--project] [--since 7d] [--out file]`, `GET /api/v1/export`, and "Export" in the ⌘K menu. CSV cells that a spreadsheet would run as formulas are escaped.
- `doctor` explains degraded modes and how to fix them: the board running on another port because 4777 was busy, and file notifications being unavailable.

### Changed
- The web app's layout and default theme credit their inspiration, next-shadcn-admin-dashboard by Mohammed Arham Khan, in the README and `NOTICE`.
- A session's model is the one that answered its latest response (sessions can switch models).
- If the system's file-notification limit is used up by other programs (Linux inotify), the daemon keeps working by polling twice a second instead of stopping, and `doctor` shows the fix.
- An event from before a session ended, read late (e.g. a subagent's own hook file), still counts but no longer reopens the session.

## [0.1.0-alpha.1] - 2026-10-10

First alpha: Claude Code support end to end.

### Added
- Releases for macOS, Linux and Windows (amd64, arm64) built by GoReleaser with the web app embedded; checksums signed keylessly with Sigstore. One-line installers (`install.sh`, `install.ps1`) verify the checksum (and the signature when cosign is available) before installing.
- Commits are linked to the agent sessions that produced them (exact when the agent ran `git commit` itself, likely when it edited the committed files, shared when several did), and a card in Review moves to Done when its work is committed. Repos are only checked when their reflog changes.
- Privacy: secrets are redacted before anything is stored (cloud and VCS tokens, private keys, JWTs, bearer headers, URL passwords, secret `KEY=value` pairs and flags, high-entropy strings in commands, custom patterns). Capture levels `minimal` / `standard` / `full` in `~/.shiplino/config.toml`; at `minimal` the hook strips content before it reaches disk.
- Project board in the web app: kanban with drag to pin (agent-driven cards can't be dropped into Running or Waiting), nested subagents, live now-doing lines, PR badges, sprint selector, filters, new backlog cards, and a quick-look drawer.
- Session page: stats, a timeline of every prompt, tool call, file edit, command and subagent (in-progress calls included), files, commands with exit codes, and per-response token usage; copy the resume command.
- Web app (Next.js static export, embedded in the binary): app shell with project switcher and live running/waiting strip, light/dark/system theme, and the all-projects overview with a Needs-you queue. `make ui` builds it; without Node the daemon serves a fallback page.
- Board per project: one card per agent session in Running / Waiting on you / Review / Done / Failed (from live status), subagents nested, drag to pin, manual Backlog cards, automatic weekly sprints with rollover, sprint report, and an all-projects overview with a "Needs you" queue.
- Projects: every session is assigned to a project automatically (git remote, else the repo's common dir so worktrees group together, else the nearest folder with a project marker, else Unsorted), with its branch. `GET /api/v1/projects` lists projects with live counts and cost; `GET /api/v1/sessions?project=` filters.
- `shiplino status`, `ls`, `open`, `doctor [--fix]`, `pause [--for 30m]` and `resume`. `doctor` checks the binary, agent hooks, daemon, backlog, parse errors and the last event per agent, and can repair hooks and the service.
- The daemon runs at login and restarts on crash: systemd user service (XDG autostart fallback) on Linux, LaunchAgent on macOS, Task Scheduler on Windows. `setup` waits until it answers; `uninstall` removes it.
- `shiplino setup` / `shiplino uninstall`: installs the binary to `~/.shiplino/bin`, connects Claude Code by adding hooks to its user settings (backed up first, order-preserving, idempotent, never touches a file it can't parse exactly), and self-tests the hook.
- `shiplino hook`: records each agent hook event to an on-disk spool. It prints nothing and always exits 0, so it adds no tokens.
- `shiplino daemon`: turns spool lines into stored events and live sessions (SQLite), exactly once across restarts.
- Claude Code support: hook events (sessions, prompts, tools, files, shell commands, waiting states, subagents) and transcript token usage with cost.
- Cost per session: computed from transcript usage × a bundled price table (cache tiers, fast mode, US inference, web-search fees), reconciled with the agent's own reported total when available. See docs/cost.md.
- Local API on `127.0.0.1:4777` with live WebSocket updates and a placeholder session page.
- User and contributor docs in `docs/`.
- Repository skeleton: Go module, package layout, web/SDK/plugin folders, event JSON Schema, CI and community files.

[Unreleased]: https://github.com/elephaant/shiplino/compare/v0.1.0-alpha.1...HEAD
[0.1.0-alpha.1]: https://github.com/elephaant/shiplino/releases/tag/v0.1.0-alpha.1
