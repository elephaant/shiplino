# @shiplino/sdk (TypeScript)

Report what your own agent does to Shiplino: sessions, turns, tool calls, shell commands, file edits, token usage, waiting for the user, and subagents. Node 20+, ESM, typed, no runtime dependencies.

## Install

Not on npm yet. Build it from this repository:

```bash
cd sdk/ts && npm install && npm run build
npm install /path/to/shiplino/sdk/ts   # in your project
```

## Use

```ts
import { Shiplino } from "@shiplino/sdk";

const shiplino = new Shiplino({ agent: "release-bot" });

const session = shiplino.session({ title: "Cut the 1.4 release" });
session.turn("Cut the 1.4 release");

const call = session.tool("Bash", { command: "npm test" });
// … run it …
call.end(true); // or call.end(false, "2 tests failed"); the duration is measured for you

session.shell("npm test", 0, 5400);
session.fileEdit("/home/dev/app/CHANGELOG.md", 12, 0);
session.usage({ model: "claude-sonnet-5", inputTokens: 1200, outputTokens: 300, cacheRead: 9000, costUsd: 0.0081 });

session.waiting("Publish to npm?");
session.resumed();

const review = session.subagent("reviewer"); // a child session with the same methods
review.end();

session.end(); // or session.end("error", "out of retries")
await shiplino.close(); // optional: flushes now (do it before process.exit())
```

| Method | Records |
|--------|---------|
| `shiplino.session({title?, cwd?, model?, id?})` | a new session (`cwd` defaults to `process.cwd()` and puts the card in that project) |
| `.turn(prompt)` / `.endTurn(status?)` | a turn; a new turn ends the previous one |
| `.tool(name, input)` → `.end(ok, error?)` | a tool call, timed for you. Known names (`Bash`, `Read`, `Edit`, `Grep`, `WebFetch`, `mcp__…`) get their kind; others are `other` |
| `.shell(command, exitCode, durationMs?)` | a shell command |
| `.fileEdit(path, added, removed)` | a file edit with your line counts |
| `.usage({model, inputTokens, outputTokens, cacheRead, cacheWrite, costUsd?})` | one model response. With `costUsd`, the session shows your cost as reported; without it, the daemon prices the tokens from its price table |
| `.waiting(message)` / `.resumed()` | the card moves to "Waiting on you" and back |
| `.subagent(type)` | a nested subagent card; `.end()` it when done |
| `.setTitle(title)` | renames the card |
| `.end(status?, error?)` | ends the session; `"error"` marks it failed |

## How it behaves

- **Finds the daemon by itself.** The token is read from `~/.shiplino/token` and the port from `~/.shiplino/port` (`$SHIPLINO_HOME` if set; `%USERPROFILE%\.shiplino` on Windows). Pass `url` and `token` to override.
- **Never breaks your agent.** No method throws. If Shiplino isn't installed, the client warns once and does nothing.
- **Batched in the background.** Events are sent every second (`flushIntervalMs`) or every 100 events (`batchSize`), and once more when the process is about to exit. Use `await shiplino.close()` before `process.exit()`, which skips that last chance.
- **Safe retries.** Every event carries a stable id, so a batch sent twice is stored once.
- **Bounded when the daemon is down.** Events wait in a queue of up to 10,000 (`maxQueue`), then the oldest are dropped. `shiplino.stats` shows `queued`, `sent`, `duplicates`, `dropped`, `rejected` and `lastError`; warnings go to `console.warn` once per kind (or to `onWarning`).
- **Redaction is the daemon's job.** Secrets are redacted and the capture level applied before anything is stored, the same as for hook events.

## Develop

```bash
npm install
npm test                                        # builds, then runs node:test
SHIPLINO_BIN=../../bin/shiplino npm test        # also runs the test against a real daemon (temp home)
```
