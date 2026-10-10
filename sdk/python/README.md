# shiplino (Python SDK)

Report what your own agent does to Shiplino: sessions, turns, tool calls, shell commands, file edits, token usage, waiting for the user, and subagents. Python 3.9+, typed, standard library only.

## Install

Not on PyPI yet. Install it from this repository:

```bash
pip install /path/to/shiplino/sdk/python
```

## Use

```python
from shiplino import Shiplino

shiplino = Shiplino(agent="release-bot")

with shiplino.session(title="Cut the 1.4 release") as session:  # an exception marks it failed
    session.turn("Cut the 1.4 release")

    with session.tool("Bash", {"command": "pytest"}):  # timed; an exception ends it with ok=False
        run_tests()

    call = session.tool("Read", {"file_path": "/home/dev/app/setup.py"})
    call.end(True)  # or call.end(False, "not found")

    session.shell("pytest", 0, 5400)
    session.file_edit("/home/dev/app/CHANGELOG.md", 12, 0)
    session.usage("claude-sonnet-5", input_tokens=1200, output_tokens=300, cache_read=9000, cost_usd=0.0081)

    session.waiting("Publish to PyPI?")
    session.resumed()

    review = session.subagent("reviewer")  # a child session with the same methods
    review.end()
```

Without `with`, call `session.end()` (or `session.end("error", "out of retries")`).

| Method | Records |
|--------|---------|
| `shiplino.session(title=None, cwd=None, *, model=None, id=None)` | a new session (`cwd` defaults to the current directory and puts the card in that project) |
| `.turn(prompt)` / `.end_turn(status="ok")` | a turn; a new turn ends the previous one |
| `.tool(name, input)` → `.end(ok, error=None)` | a tool call, timed for you. Known names (`Bash`, `Read`, `Edit`, `Grep`, `WebFetch`, `mcp__…`) get their kind; others are `other` |
| `.shell(command, exit_code, duration_ms=None)` | a shell command |
| `.file_edit(path, added, removed)` | a file edit with your line counts |
| `.usage(model, input_tokens, output_tokens, cache_read, cache_write, cost_usd=None)` | one model response. With `cost_usd`, the session shows your cost as reported; without it, the daemon prices the tokens from its price table |
| `.waiting(message)` / `.resumed()` | the card moves to "Waiting on you" and back |
| `.subagent(type)` | a nested subagent card; `.end()` it when done |
| `.set_title(title)` | renames the card |
| `.end(status="ok", error=None)` | ends the session; `"error"` marks it failed |

## How it behaves

- **Finds the daemon by itself.** The token is read from `~/.shiplino/token` and the port from `~/.shiplino/port` (`$SHIPLINO_HOME` if set; `%USERPROFILE%\.shiplino` on Windows). Pass `url=` and `token=` to override. Proxy environment variables are ignored: the daemon is always local.
- **Never breaks your agent.** No method raises. If Shiplino isn't installed, the client warns once and does nothing.
- **Batched in the background.** A daemon thread sends events every second (`flush_interval`) or every 100 events (`batch_size`), and they're flushed at interpreter exit. Call `shiplino.close()` to flush earlier.
- **Safe retries.** Every event carries a stable id, so a batch sent twice is stored once.
- **Bounded when the daemon is down.** Events wait in a queue of up to 10,000 (`max_queue`), then the oldest are dropped. `shiplino.stats` shows `queued`, `sent`, `duplicates`, `dropped`, `rejected` and `last_error`; warnings go to the `shiplino` logger once per kind (or to `on_warning`).
- **Redaction is the daemon's job.** Secrets are redacted and the capture level applied before anything is stored, the same as for hook events.

## Develop

```bash
python -m unittest discover -s tests
SHIPLINO_BIN=../../bin/shiplino python -m unittest discover -s tests   # also against a real daemon (temp home)
```
