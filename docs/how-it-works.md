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

1. Each pass (on a file change, or every 2 s) reads the new lines of every spool file and transcript. Files are read, parsed and redacted in parallel. One file holds one session and is read by one goroutine at a time, so a session's events stay in order. Some agents (Cline) rewrite a whole JSON file instead of appending to it; such a file is read again in full when it changes (at most every 5 s), and its events have stable keys, so a reread adds nothing twice.
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

## Which committed lines an agent wrote

When a commit is linked to sessions, Shiplino also splits the lines it added:

1. It reads the commit's own diff locally (`git show`, no context lines, no external diff or text-conversion tools) for the committed files the linked sessions edited.
2. It compares each added line with the lines that the sessions' file edits added (the stored diff of each edit, made before the commit). Lines are compared without surrounding whitespace, and each edited line matches at most one committed line. Diffs are stored redacted, so a committed line that holds a secret is compared in its redacted form.
3. The commit gets three counts: **agent** lines (matched), **other** lines (in files the agent didn't edit, or not matched) and **unknown** lines (see below). They always add up to the commit's added lines.

**Line attribution needs capture level `full`.** Below `full`, edits keep their line counts but not their diffs, so Shiplino only knows which committed files the agent edited: their lines count as unknown and the badge says **Unknown**. With diffs the badge says **Observed**. A commit where only some edits have a diff (an edit recorded before you switched to `full`, a diff cut at 64 KB, a secret file) is partly unknown.

Limits, so a big commit never slows the daemon: files adding more than 5,000 lines, binary files and diffs over 4 MB aren't compared (their lines count as unknown when the agent edited them), at most 200 edits per file are read, and lines are cut at 4 KB. Lines that a tool run by the agent wrote (a generator, a formatter, `sed`) aren't file edits, so they count as other lines. A line an agent added and later removed again can still match an identical line you typed.

The session page and the card sheet show "N of M lines by agent" for each commit, and Insights shows the agents' share of committed lines per day, agent and project. Commits recorded before this existed have no split.

### Git notes (opt-in)

`shiplino git notes` writes each recorded commit's counts (lines added, agent, other and unknown lines, agent-edited file count, agent names; no code, prompts or paths) as a git note under `refs/notes/shiplino` in the current repository. It writes only after you set `notes = true` under `[git]` in `~/.shiplino/config.toml`; `--dry-run` prints the notes instead. Running it again replaces Shiplino's notes. Shiplino never pushes notes: to share them, push the ref yourself (`git push origin refs/notes/shiplino`). Read them with `git log --notes=shiplino`.

### Agent Trace

[Agent Trace](https://agent-trace.dev/) (version 0.1.0, an RFC published in January 2026; checked 2026-10-11) is a proposed open format for recording which line ranges of a file came from AI or humans, per revision. Shiplino doesn't export it yet. Its data maps onto it like this: the commit is the record's `vcs.revision`, each file in the per-file split is a `files[]` entry with its project-relative `path`, the agent is the `contributor` (`type: "ai"`, the session's model as `model_id`), and lines that didn't match are `human` or `unknown`. Agent Trace wants line ranges, not counts, so an exporter would keep the matched lines' positions from the commit's diff.

## Where each value comes from

The board marks values with a small evidence badge (an icon and a border style, with a tooltip that names the source):

| Badge | Meaning | Examples |
|-------|---------|----------|
| **Reported** | The agent said so | its own cost total, line counts from its own diff, subagent ids, plan limit percentages |
| **Observed** | Shiplino saw it happen | status from hooks or transcripts, a commit the agent ran itself, a project found by git |
| **Inferred** | Shiplino derived it | cost from tokens × list prices, idle after 30 quiet minutes, a commit linked because the session edited its files, a project grouped by folder, line counts from the edit text |
| **Unknown** | Nothing records it | no token usage, an unpriced model |

Board cards show the cost badge and badges for inferred values only; the card sheet and the session page show them all.

## Privacy

- Everything stays on your machine. Nothing is uploaded unless you turn on sync (see [sync-protocol.md](sync-protocol.md)) or an integration.
- Secrets (API keys, tokens, passwords, private keys, `KEY=value` credentials, high-entropy strings in commands) are redacted **before anything is stored**, and replaced with markers like `«redacted:github_token»` so the timeline still reads well. Add your own patterns under `[redaction] extra_patterns` in `~/.shiplino/config.toml`.
- Capture levels (`capture_level` in `~/.shiplino/config.toml`) control how much is recorded:
  - `minimal`: timing, tool names, file paths, exit codes, tokens and cost. No prompts, commands, messages or outputs; the hook strips them before writing to disk.
  - `standard` (default): also prompts (truncated), commands and short summaries. File edits keep their line counts, not the diff.
  - `full`: everything Shiplino captures, including the diff of each file edit (up to 64 KB per edit, never for secret files such as `.env*` or keys).
- The local API listens on `127.0.0.1` only and requires a token.
