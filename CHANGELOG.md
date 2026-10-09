# Changelog

All notable changes to this project will be documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project uses [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- `shiplino setup` / `shiplino uninstall`: installs the binary to `~/.shiplino/bin`, connects Claude Code by adding hooks to its user settings (backed up first, order-preserving, idempotent, never touches a file it can't parse exactly), and self-tests the hook.
- `shiplino hook`: records each agent hook event to an on-disk spool. It prints nothing and always exits 0, so it adds no tokens.
- `shiplino daemon`: turns spool lines into stored events and live sessions (SQLite), exactly once across restarts.
- Claude Code support: hook events (sessions, prompts, tools, files, shell commands, waiting states, subagents) and transcript token usage with cost.
- Cost per session: computed from transcript usage × a bundled price table (cache tiers, fast mode, US inference, web-search fees), reconciled with the agent's own reported total when available. See docs/cost.md.
- Local API on `127.0.0.1:4777` with live WebSocket updates and a placeholder session page.
- User and contributor docs in `docs/`.
- Repository skeleton: Go module, package layout, web/SDK/plugin folders, event JSON Schema, CI and community files.
