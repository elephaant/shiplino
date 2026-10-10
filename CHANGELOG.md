# Changelog

All notable changes to this project will be documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- Budgets: daily, monthly and per-project spend limits in `config.toml` ([budget]) with desktop alerts at 80% and when reached, and an optional daily digest of the agents' work. Shown on the Settings page.
- GitHub pull requests (opt-in, `[integrations.github]`): cards and session pages show each PR's state, CI checks and review decision. A session's branch PR is linked even when the agent didn't report it, and a merged PR moves its card to Done.
- TypeScript (`@shiplino/sdk`) and Python (`shiplino`) SDKs for custom agents: sessions, turns, timed tool calls, shell commands, file edits, usage, waiting and subagents, batched in the background with idempotent retries and a bounded queue. They never throw into the agent. The ingest API now prices usage events that carry tokens but no cost.
- OpenCode support: `shiplino setup` installs an observe-only plugin (`~/.config/opencode/plugins/shiplino.js`) that records sessions, subagents, prompts, tool calls, file edits, permission prompts and OpenCode's own tokens and cost (shown as reported cost).
- Cline support: `shiplino setup` adds hook scripts to `~/Documents/Cline/Hooks` (the VS Code/JetBrains extension and the CLI; also the extension's own Documents folder when it's on OneDrive or a custom XDG folder). It records tasks, prompts, tool calls with Cline's own timings, file reads and edits, commands (with exit codes from the CLI), MCP calls and subagents. Your own Cline hook files are kept, and setup and doctor say which events they occupy. No token usage or cost yet.
- Browser end-to-end tests (`make e2e`, CI job `e2e`): Playwright drives the web app against the real binary and a daemon fed through the hook shim, covering the overview, board drag/pin and refused drops, live updates, search, session detail and theme. The board now shows when a column won't take a dragged card and explains why, and a hydration error from the theme toggle is fixed.

### Fixed
- Claude Code token counts. Responses copied into resumed or continued sessions counted again (input was ~24% high), and responses written over several lines kept their first, smaller count (output ~8% low). Each response now counts once at its final size, and stored history is cleaned and rebuilt on upgrade.

### Changed
- **Cloud sync sends metadata only.** Prompts, replies, shell commands, tool input and output, errors, diffs, commit messages and file contents never leave your machine, at any setting. Only an allow list of metadata fields is sent (sessions, status, timing, tokens and cost, tool names, outcomes, project-relative paths, git references), so fields added later stay local until reviewed. Session titles are sent only with the new `[sync] send_titles = true`. The old `[sync] capture_level` and `send_user` settings are ignored, and `shiplino sync status` says so.
- **Shiplino is now licensed under the Apache License 2.0** (previously the Functional Source License). Releases up to 0.1.0-alpha.2 stay under their original license. Source files no longer carry per-file license headers; `LICENSE` covers the repository.

## [0.1.0-alpha.2] - 2026-10-10

Codex, Cursor, Gemini CLI, Copilot CLI, Windsurf and Aider support; search, notifications, Insights, Timeline and Settings; history backfill; opt-in cloud sync client.

### Added
- Pricing supports announced price changes: models can list dated rates, and every response is priced at the rates in effect when it was made, so past sessions keep their cost after a change. Adds Gemini 3.6/3.8 Flash's 2027-01-01 prices.
- Windsurf: the JetBrains plugin is supported too. `shiplino setup`, `doctor` and `uninstall` handle its `~/.codeium/hooks.json` beside the editor's `~/.codeium/windsurf/hooks.json`, and it appears as "Windsurf (JetBrains)" in settings.
- `POST /api/v1/ingest` for custom agents (JSON array or NDJSON, per-event errors, idempotent retries) and an OTLP/HTTP receiver: `/v1/logs` is decoded with a built-in, dependency-free decoder, and `/v1/metrics` and `/v1/traces` are counted. It records Claude Code's exported API requests, prompts and tool results. Telemetry cost is compared with transcript cost, never added. `shiplino doctor` shows the endpoints and token location. See `docs/ingest.md`.
- Sync at the default `minimal` level now sends file paths relative to the project (or `…/name` for paths outside it), and no longer sends absolute project folders or your OS user name. At `standard` and `full`, the user name is sent only with the new `[sync] send_user = true`.
- Timeline page: a live Gantt of sessions and subagents (running, waiting on you, idle) with hour/day/week zoom, and a "Changes" tab on the session page with per-file +/− and, at capture level `full`, each edit's redacted diff (up to 64 KB; never for secret files).
- Board, session page and Insights: sessions whose agent records no token usage (e.g. Windsurf, Copilot CLI, Cursor transcripts, wrapped CLIs) now show "no cost data" instead of a blank or $0 cost, and Insights counts them separately from sessions whose model isn't priced.
- Windsurf (Cascade) support: `shiplino setup` adds observe-only hooks to `~/.codeium/windsurf/hooks.json` and records prompts, turns, file reads and edits (with ± lines), commands and MCP calls. Windsurf's hooks don't report token usage, so its sessions show no cost.
- GitHub Copilot CLI support: `shiplino setup` writes observe-only hooks to `~/.copilot/hooks/shiplino.json` and records sessions, prompts, tool calls, shell commands, file edits, subagent ends, errors and permission prompts (no token usage yet).
- Gemini CLI support: `shiplino setup` registers observe-only hooks in `~/.gemini/settings.json`. Sessions, turns, tool calls, shell commands, file edits (with Gemini's own line counts), MCP calls and permission waits are recorded live. Token usage, model and session titles come from Gemini's chat recordings, priced with Gemini API list prices, and past sessions can be backfilled.
- Cursor: sessions that ran without hooks (before setup, or with hooks disabled) are rebuilt from Cursor's agent transcripts, including subagents, and `shiplino backfill` imports them. Transcript data is coarser than hook data: times are to the minute, there are no tokens or exit codes, and line counts are computed.
- `shiplino wrap -- <command>` records any CLI agent without hooks. The command runs unchanged (same terminal, I/O, exit code and signals), and the session shows on the board with its directory, redacted command, exit code, duration and git commits. For Aider, prompts, Aider's own token and cost report, edited files and auto commits are read from its chat history.
- Opt-in cloud sync client: `shiplino sync login|status [--dry-run]|allow|deny|logout`. It signs in with a device code and keeps tokens in the OS keychain (or a visible 0600 file where there's none). Only projects you allow are sent, stripped to the sync capture level (minimal by default) and redacted again, in gzip batches with resume, retry and backoff. Sync state is shown in `doctor` and on the Settings page. The wire protocol is in `docs/sync-protocol.md`.
- Daemon: much faster under heavy load. Files are parsed in parallel and written in batched transactions, WAL checkpoints no longer stall writes, and boards read only the current sprint's sessions. Adds a load test (`make bench`) and a "Performance" section in `docs/how-it-works.md`.
- OpenAI Codex support. `shiplino setup` adds hooks to `~/.codex/hooks.json` for all 12 Codex hook events (backed up, idempotent, your own hooks kept); Codex asks once to trust them under `/hooks`. Sessions from the Codex desktop app, which run without hooks, are found in `~/.codex/sessions` and read from their rollout files: prompts, turns, commands with exit codes, file changes with Codex's own line counts, MCP calls, model changes and per-response token usage (Codex's `token_usage_record`). When a session has hooks, its activity comes from the hooks and its rollout only adds usage, so nothing is counted twice.
- Cursor support (IDE agent and `cursor-agent`). `shiplino setup` adds observe-only hooks to `~/.cursor/hooks.json`: never permission hooks, never `failClosed`. It records prompts, turns, tool calls with durations, shell commands with exit codes, file edits, MCP calls, subagents (nested under their parent) and compaction. Token usage is recorded when Cursor includes it in `afterAgentResponse`.
- Cursor also runs Claude Code and Codex hooks. Those copies are recognized and skipped, so a Cursor session is never recorded twice.
- `doctor` and `uninstall` cover every connected agent.
- Search: prompts, commands, file paths, session titles and commit messages are indexed (SQLite FTS5, existing data included) and searchable with ⌘K / Ctrl K in the web app, `shiplino search <words>` and `GET /api/v1/search`. Every word must match, as a prefix.
- Export: `shiplino export [--format csv|json] [--project] [--since 7d] [--out file]`, `GET /api/v1/export`, and "Export" in the ⌘K menu. CSV cells that a spreadsheet would run as formulas are escaped.
- Desktop notifications on Linux, macOS and Windows: an agent is waiting on you (only if it's still waiting after 3 seconds), finished a turn that ran at least 30 seconds, or failed. Several at once are grouped, and a session isn't notified twice within 30 seconds. Configure under `[notify]` in `config.toml`; check with `shiplino notify test` and `doctor`.
- Insights page: spend, sessions, time agents spent working, time they waited on you and code changed, each compared with the previous period; a per-day chart (spend by agent, sessions, time); breakdowns by agent, project, model and tool; and where the dollar figures come from (reported by the agent, computed, or none). `GET /api/v1/insights?days=&project=`.
- Settings page: pause and resume recording, which agents are connected (and how to connect the rest), capture level and data location, notification settings with a test button, daemon health, and data export. `GET /api/v1/settings`, `POST /api/v1/pause`, `/resume`, `/notify/test`.
- Sessions record their active time: the time spent in turns, using the agent's own turn duration when it reports one, so idle time between prompts isn't counted.
- When an update changes how sessions are computed, the daemon rebuilds them from the stored events once at startup, so past sessions benefit too.
- A running session with no activity for 30 minutes (2 hours while a tool such as a long build is still running) becomes idle and leaves the Running column: to Review if it changed files, otherwise to Done. Its next event brings it back.
- Cards roll up their subagents: a card shows Waiting if any subagent waits on you (with what it's asking), Running if any still works, and counts subagents' files, lines and tool calls.
- Codex sessions now show cost: OpenAI list prices for gpt-5.6-terra, gpt-5.6-luna, gpt-5.5 and gpt-5.3-codex, including long-context (over 272K prompt tokens), fast and flex rates.
- Claude Code sessions the hooks didn't see (from before setup, or with hooks missing) are rebuilt from their transcripts: prompts, turns, tool calls, commands with exit codes, file edits with Claude Code's own diff counts, subagents and the PRs it reported. Recent transcripts are found automatically.
- `shiplino backfill [--since 30d]` imports agent history from before setup, and `shiplino setup` imports the last 30 days so the board isn't empty on first run.
- `doctor` explains degraded modes and how to fix them: the board running on another port because 4777 was busy, and file notifications being unavailable.

### Changed
- Model ids match a priced model only when followed by a date or a variant tag, so a different model such as `…-mini` is never priced as its larger sibling.
- The web app's layout and default theme credit their inspiration, next-shadcn-admin-dashboard by Mohammed Arham Khan, in the README and `NOTICE`.
- A session's model is the one that answered its latest response (sessions can switch models).
- If the system's file-notification limit is used up by other programs (Linux inotify), the daemon keeps working by polling twice a second instead of stopping, and `doctor` shows the fix.
- An event from before a session ended, read late (e.g. a subagent's own hook file), still counts but no longer reopens the session.

### Fixed
- On Windows, the daemon could stop reading new events for good when a file changed while it was adding a folder watch (a deadlock with the file watcher).
- At the minimal capture level, git commits showed "Waiting for you" as their subject. Only waiting events keep that text now; other messages are dropped.

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

[Unreleased]: https://github.com/elephaant/shiplino/compare/v0.1.0-alpha.2...HEAD
[0.1.0-alpha.2]: https://github.com/elephaant/shiplino/compare/v0.1.0-alpha.1...v0.1.0-alpha.2
[0.1.0-alpha.1]: https://github.com/elephaant/shiplino/releases/tag/v0.1.0-alpha.1
