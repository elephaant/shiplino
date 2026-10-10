# How Shiplino works

## Capture: hooks, transcripts and git

Modern coding agents (Claude Code, Codex, Cursor, Gemini CLI, Copilot CLI, Windsurf, Cline, …) can run a program at lifecycle moments such as session start, before/after a tool call, and turn end. These are called **hooks**.

`shiplino setup` registers one program, `shiplino hook`, in each installed agent's **user-level** hook config. When a hook fires:

1. The agent runs `shiplino hook --agent <name>` and passes the event as JSON on stdin.
2. Shiplino appends one line to `~/.shiplino/spool/<agent>/<session>.jsonl` and exits.
3. A local background service (the daemon) reads new lines and also reads the agents' own transcript files (for exact token counts) and git (for commits).
4. Everything is converted to one [event format](event-format.md) and stored in a local SQLite database.
5. The board is served at `http://localhost:4777` and updates live.

Sessions that ran without hooks (before setup, or with hooks turned off) are rebuilt from the agents' transcript files instead; `shiplino backfill` imports older history. When a session has hooks, its activity comes from the hooks only, so nothing is counted twice. Cursor's transcripts are coarser than its hooks: times are to the minute, there are no token counts or exit codes, every tool call counts as succeeded, and line counts are computed from the edits.

### Agents without hooks: `shiplino wrap`

For a CLI agent with no hooks (Aider, or anything else), start it through Shiplino:

```bash
shiplino wrap -- aider --model sonnet
shiplino wrap --agent goose --title "fix login" -- goose session
```

The command runs exactly as if you typed it: same terminal, input, output and exit code, and Ctrl-C reaches it once. Shiplino writes the run's start (directory, redacted command line) and end (exit code, duration) to the spool, and git commits in that directory are linked as for any session. A non-zero exit marks the session failed. If recording fails, the command still runs and Shiplino says so once, after it exits.

For **Aider**, Shiplino also follows its chat history file (`.aider.chat.history.md` at the repo root, or `--chat-history-file`) while it runs, and records each prompt, Aider's own token and cost report, the files it edited and its auto commits. Only those lines are copied; the model's answers stay in Aider's file. A history path set only in `.aider.conf.yml` or `.env` isn't seen: pass it on the command line.

## Zero tokens

The model never knows Shiplino exists:

- Hooks are run by the agent **program**, not the model.
- `shiplino hook` **prints nothing and always exits 0**, so nothing is added to the model's context and nothing is blocked.
- Shiplino adds no MCP tools, no instructions to `CLAUDE.md`/`AGENTS.md`, and makes no LLM calls by default.

## Never in the way

- The hook path does no network, database or config work. It just appends a line.
- If the daemon is stopped, events wait on disk and are processed later.
- Shiplino only observes. It never blocks or changes what an agent does.

## Performance

The daemon is one pipeline with a single database writer:

1. Each pass (on a file change, or every 2 s) reads the new lines of every spool file and transcript. Files are read, parsed and redacted in parallel. One file holds one session and is read by one goroutine at a time, so a session's events stay in order.
2. One goroutine applies the events to the task engine and writes them to SQLite in transactions of up to 500 events, together with the read offsets they cover. A crash or restart neither loses nor repeats an event. Folding events into sessions takes about 1% of the time, so the engine isn't split up.
3. The live view gets each changed session at most every 100 ms.

SQLite runs in WAL mode. Checkpoints, which copy the WAL back into the database file, run on their own connection beside the writer, so commits don't wait for disk syncs. A board reads only the sessions it can show (the sprint's, plus anything still open or moved by hand), not the project's whole history.

`make bench` runs the load tests in [bench/](../bench/README.md). On an 8-core laptop (i5-11300H) with the database on tmpfs:

| | Target | Measured |
|---|---|---|
| Hook → live view, p95 (50 sessions × 4 subagents × 10 tool calls/s, 4,000 hooks/s) | < 500 ms | 135 ms (p50 76 ms) |
| Ingest of a 200,000-line backlog | > 20,000 events/s | 35,000 events/s |
| Board query during that load, p95 | < 30 ms | 1.2 ms |
| Board with 2,000 past sessions × 4 subagents, p95 | < 30 ms | 8.7 ms |

On disk, the limit is how fast the disk syncs. On the same laptop's SSD, while it was also swapping and running other builds, ingest reached about 9,500 events/s and hook → live view p95 was about 1.9 s at 4,000 hooks/s. Everyday loads are far lighter: one agent rarely makes more than a few tool calls per second.

## Projects, subagents, parallel work

Every repo becomes a project with its own board. Many agents and subagents can run at the same time. Each one is tracked separately (by session and subagent id) and rolled up into its parent card.

## Privacy

- Everything stays on your machine. Nothing is uploaded unless you turn on sync (see [sync-protocol.md](sync-protocol.md)) or an integration.
- Secrets (API keys, tokens, passwords, private keys, `KEY=value` credentials, high-entropy strings in commands) are redacted **before anything is stored**, and replaced with markers like `«redacted:github_token»` so the timeline still reads well. Add your own patterns under `[redaction] extra_patterns` in `~/.shiplino/config.toml`.
- Capture levels (`capture_level` in `~/.shiplino/config.toml`) control how much is recorded:
  - `minimal`: timing, tool names, file paths, exit codes, tokens and cost. No prompts, commands, messages or outputs; the hook strips them before writing to disk.
  - `standard` (default): also prompts (truncated), commands, short summaries and the diff of each file edit (up to 64 KB per edit, never for secret files such as `.env*` or keys).
  - `full`: everything Shiplino captures.
- The local API listens on `127.0.0.1` only and requires a token.
