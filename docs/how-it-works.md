# How Shiplino works

## Capture: hooks, transcripts and git

Modern coding agents (Claude Code, Codex, Cursor, Gemini CLI, Copilot CLI, Windsurf, Cline, …) can run a program at lifecycle moments such as session start, before/after a tool call, and turn end. These are called **hooks**.

`shiplino setup` registers one program, `shiplino hook`, in each installed agent's **user-level** hook config. When a hook fires:

1. The agent runs `shiplino hook --agent <name>` and passes the event as JSON on stdin.
2. Shiplino appends one line to `~/.shiplino/spool/<agent>/<session>.jsonl` and exits.
3. A local background service (the daemon) reads new lines and also reads the agents' own transcript files (for exact token counts) and git (for commits).
4. Everything is converted to one [event format](event-format.md) and stored in a local SQLite database.
5. The board is served at `http://localhost:4777` and updates live.

## Zero tokens

The model never knows Shiplino exists:

- Hooks are run by the agent **program**, not the model.
- `shiplino hook` **prints nothing and always exits 0**, so nothing is added to the model's context and nothing is blocked.
- Shiplino adds no MCP tools, no instructions to `CLAUDE.md`/`AGENTS.md`, and makes no LLM calls by default.

## Never in the way

- The hook path does no network, database or config work. It just appends a line.
- If the daemon is stopped, events wait on disk and are processed later.
- Shiplino only observes. It never blocks or changes what an agent does.

## Projects, subagents, parallel work

Every repo becomes a project with its own board. Many agents and subagents can run at the same time. Each one is tracked separately (by session and subagent id) and rolled up into its parent card.

## Privacy

- Everything stays on your machine. Nothing is uploaded unless you turn on sync or an integration.
- Secrets (API keys, tokens, passwords, private keys) are redacted before anything is stored.
- Capture levels (`minimal`, `standard`, `full`) control how much is recorded, e.g. no prompts at all with `minimal`.
- The local API listens on `127.0.0.1` only and requires a token.
