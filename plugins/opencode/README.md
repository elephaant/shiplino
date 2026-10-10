# OpenCode plugin

`shiplino.js` records what [OpenCode](https://opencode.ai) does on the local Shiplino board: sessions, subagents (task tool), prompts, tool calls, file edits, permission prompts and questions, and the tokens and cost OpenCode reports for each response.

`shiplino setup` installs it as `~/.config/opencode/plugins/shiplino.js` (`$XDG_CONFIG_HOME/opencode/plugins/` when set) with the absolute path of the `shiplino` binary filled in, and `shiplino uninstall` removes it. Restart running OpenCode sessions afterwards. To install it by hand, copy the file there; it then runs `shiplino` from `PATH` (or `$SHIPLINO_BIN`).

It is observe-only. It uses only OpenCode's `event` hook, never throws, never changes tool input or output, and prints nothing. For each event worth recording it starts `shiplino hook --agent opencode` with a small JSON payload on stdin and doesn't wait for it. The payload format and its mapping to Shiplino events are documented in [`pkg/adapters/opencode`](../../pkg/adapters/opencode/doc.go).

Tests: `node --test plugins/opencode/shiplino.test.mjs` (also run by `go test ./plugins/opencode`). `UPDATE=1` rewrites the adapter fixture `pkg/adapters/opencode/testdata/v1.18/hooks.jsonl` from `bus.jsonl`.
