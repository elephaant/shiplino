# Changelog

All notable changes to this project will be documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
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
