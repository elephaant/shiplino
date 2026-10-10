// Package opencode is the OpenCode adapter ("opencode").
//
// Checked on 2026-10-10 against the official docs (opencode.ai/docs/plugins)
// and the source of OpenCode v1.18.35 (packages/plugin, packages/schema):
//
//   - OpenCode has no command hooks. It loads JS/TS plugins: every
//     {plugin,plugins}/*.{js,ts} file in its global config directory
//     ($XDG_CONFIG_HOME/opencode, else ~/.config/opencode, on every OS)
//     and in a project's .opencode/. Each exported function gets
//     {project, directory, worktree, client, $} and returns hooks.
//   - Most hooks run inside the agent loop and can change it (throwing in
//     tool.execute.before blocks a tool, chat.params and the experimental
//     hooks rewrite requests, permission.ask answers permissions). Only the
//     `event` hook is a pure observer: OpenCode calls it with every bus
//     event and doesn't wait for it. Shiplino's plugin uses `event` and
//     nothing else.
//
// Shiplino installs one file, plugins/shiplino.js (plugins/opencode in this
// repository) with the binary path filled in. For each event worth
// recording it starts `shiplino hook --agent opencode` without waiting and
// writes a compact payload: {session_id, hook_event_name, timestamp (ms),
// cwd, lineage (ancestor session ids, root first), version, ...}.
//
//	session.created        session.start; a child session: subagent.start on its parent
//	session.updated        session.update (title), sent when the title changes
//	session.deleted        session.end (subagent.end for a child)
//	message.part.updated   text of a user message: turn.start (prompt)
//	                       tool part running/completed/error: tool.start / tool.end
//	                       (+ shell.exec, file.read, file.edit)
//	message.updated        completed assistant message: usage with OpenCode's
//	                       own tokens and cost
//	permission.asked       waiting.start (permission); permission.updated in
//	                       older versions
//	permission.replied     waiting.end (denied when the reply is "reject")
//	question.asked         waiting.start (question); question.replied/rejected: waiting.end
//	session.idle           turn.end (subagent.end for a child)
//	session.compacted      compact
//	session.error          error (aborts by the user are skipped)
//
// Subagents (the task tool) are child sessions with a parentID. A child's
// events are recorded on its root session with actor id
// "opencode:<root>/sub:<child>" (one /sub: per level).
//
// Usage: each assistant message carries tokens {input, output, reasoning,
// cache {read, write}} and cost, priced by OpenCode itself from its model
// catalog. Input excludes cached tokens and output excludes reasoning.
// That cost is used as reported (cost_source "reported"); when it is 0
// (models without prices, subscriptions) Shiplino's price table is the
// fallback.
//
// Not read: OpenCode's own history (opencode.db, SQLite, in
// $XDG_DATA_HOME/opencode or ~/.local/share/opencode; JSON files under
// storage/ in older versions). Its schema is internal and changes between
// releases, so sessions from before the plugin was installed aren't
// backfilled.
//
// Tested with OpenCode 1.18.
package opencode
