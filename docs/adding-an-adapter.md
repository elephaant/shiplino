# Adding an agent adapter

An adapter connects one agent to Shiplino. It lives in `pkg/adapters/<agent>/`.

## What an adapter does

| File | Job |
|------|-----|
| `detect.go` | Is the agent installed? Which version? Where is its config? |
| `install.go` | Add (and remove) our hook entries in the agent's **user-level** config. Must be idempotent |
| `parse.go` | Convert native hook payloads into [events](event-format.md) |
| `transcript.go` | (optional) Read the agent's own session files for tokens and anything hooks miss: line by line from the last offset for files it appends to (`TranscriptParser`), or whole again on every change for JSON files it rewrites (`DocumentParser`; give each event a dedup key that survives rewrites, and emit a value only once it is final) |
| `conversation.go` | (optional) `ConversationReader`: read prompts, replies and tool calls back from those files on demand, for the session's Conversation tab. Nothing it reads is stored |
| `testdata/<agent-version>/` | Real, **redacted** payloads + expected events (golden files) |

## Rules (CI enforces them)

1. Register only `command`-type hooks that run `shiplino hook --agent <name>`, with the **absolute** binary path and a short timeout. Use async mode where the agent supports it.
2. Never make the hook print output, return JSON decisions, or exit non-zero.
3. Edit config files with a real parser, write atomically, back up first, and never touch a file that fails to parse.
4. Recognize our entries by the command string, so the user's own hooks are never changed.
5. Report subagents with their ids (`actor_id`, `parent_actor`) when the agent provides them. Don't guess from timing.
6. Be tolerant: ignore unknown fields and keep unknown event types as raw.

## Checklist

- [ ] detect / install / uninstall with tests on empty, existing and malformed configs
- [ ] parse with golden fixtures for at least one agent version
- [ ] subagent handling (if the agent supports subagents)
- [ ] tool-name mapping to normalized names
- [ ] `shiplino doctor` checks
- [ ] row added to the supported agents table in the main README
