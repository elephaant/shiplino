// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { after, before, beforeEach, test } from "node:test";
import { fileURLToPath } from "node:url";

import { Shiplino, toolKind } from "../dist/index.js";

const TOKEN = "t".repeat(64);

// A fake daemon: stores events by dedup key like the real one, and can be
// told to fail.
const daemon = {
  requests: [],
  keys: new Set(),
  status: 200,
  reset() {
    this.requests = [];
    this.keys = new Set();
    this.status = 200;
  },
  get events() {
    return this.requests.flat();
  },
};

const server = createServer((req, res) => {
  let body = "";
  req.on("data", (c) => {
    body += c;
  });
  req.on("end", () => {
    if (req.headers.authorization !== `Bearer ${TOKEN}`) {
      res.writeHead(401, { "content-type": "application/json" }).end('{"error":"unauthorized"}');
      return;
    }
    if (typeof daemon.status === "number" && daemon.status !== 200) {
      res.writeHead(daemon.status).end();
      return;
    }
    const events = JSON.parse(body);
    daemon.requests.push(events);
    let accepted = 0;
    for (const e of events) {
      const key = `${e.session_id}:${e.id}`;
      if (!daemon.keys.has(key)) accepted++;
      daemon.keys.add(key);
    }
    if (daemon.status === "lost") {
      // Stored, but the answer never arrives.
      res.writeHead(503).end();
      return;
    }
    res.writeHead(200, { "content-type": "application/json" });
    res.end(JSON.stringify({ accepted, duplicates: events.length - accepted, errors: [] }));
  });
});
let url;

before(async () => {
  await new Promise((r) => server.listen(0, "127.0.0.1", r));
  url = `http://127.0.0.1:${server.address().port}`;
});
after(() => server.close());
beforeEach(() => daemon.reset());

function client(opts = {}) {
  const warnings = [];
  const c = new Shiplino({ agent: "test-bot", url, token: TOKEN, flushIntervalMs: 60_000, onWarning: (m) => warnings.push(m), ...opts });
  return { c, warnings };
}

test("records a whole session in the universal format", async () => {
  const { c, warnings } = client();
  const s = c.session({ title: "Fix the login bug", cwd: "/home/dev/app" });
  s.turn("Fix the login bug");
  const call = s.tool("Read", { file_path: "/home/dev/app/login.ts" });
  call.end();
  s.tool("Bash", "npm test").end(false, "1 test failed");
  s.shell("npm test", 1, 1200);
  s.fileEdit("/home/dev/app/login.ts", 4, 2);
  s.usage({ model: "claude-opus-5-5", inputTokens: 1200, outputTokens: 300, cacheRead: 50, cacheWrite: 10, costUsd: 0.02 });
  s.usage({ model: "claude-opus-5-5", inputTokens: 100, outputTokens: 30 });
  s.waiting("Deploy to staging?");
  s.resumed();
  const sub = s.subagent("reviewer");
  sub.tool("Grep", { pattern: "TODO" }).end();
  sub.usage({ model: "claude-opus-5-5", inputTokens: 10, outputTokens: 5, costUsd: 0.01 });
  sub.end();
  s.end();
  s.turn("ignored after end");
  await c.close();

  assert.deepEqual(warnings, []);
  const ev = daemon.events;
  assert.deepEqual(
    ev.map((e) => e.kind),
    [
      "session.start", "turn.start", "tool.start", "tool.end", "tool.start", "tool.end", "shell.exec", "file.edit",
      "usage", "usage", "usage", "waiting.start", "waiting.end", "subagent.start", "tool.start", "tool.end",
      "usage", "usage", "subagent.end", "turn.end", "session.end",
    ],
  );
  for (const e of ev) {
    assert.equal(e.v, 1);
    assert.deepEqual(e.agent, { name: "test-bot" });
    assert.equal(e.session_id, ev[0].session_id);
    assert.match(e.session_id, /^test-bot:[0-9a-f-]{36}$/);
    assert.deepEqual(e.project, { cwd: "/home/dev/app" });
    assert.ok(!Number.isNaN(Date.parse(e.ts)));
  }
  assert.equal(new Set(ev.map((e) => e.id)).size, ev.length, "dedup keys are unique");
  assert.equal(c.stats.sent, ev.length);

  assert.deepEqual(ev[0].data, { title: "Fix the login bug" });
  assert.deepEqual(ev[1].data, { prompt: "Fix the login bug" });
  const [start, end] = [ev[2].data, ev[3].data];
  assert.equal(start.tool_call_id, end.tool_call_id);
  assert.deepEqual({ ...start, tool_call_id: 0 }, { tool_call_id: 0, tool: "read", tool_raw: "Read", input_summary: "/home/dev/app/login.ts" });
  assert.equal(end.ok, true);
  assert.equal(typeof end.duration_ms, "number");
  assert.equal(ev[4].data.input_summary, "npm test");
  assert.equal(ev[5].data.ok, false);
  assert.equal(ev[5].data.error, "1 test failed");
  assert.deepEqual(ev[6].data, { command: "npm test", exit_code: 1, duration_ms: 1200 });
  assert.deepEqual(ev[7].data, { path: "/home/dev/app/login.ts", op: "edit", tool: "edit", lines_added: 4, lines_removed: 2, lines_source: "reported" });
  assert.deepEqual(ev[8].data, {
    model: "claude-opus-5-5", input_tokens: 1200, output_tokens: 300, cache_read_tokens: 50, cache_write_tokens: 10,
    cost_usd: 0.02, cost_source: "reported",
  });
  assert.equal(ev[9].data.report, true);
  assert.equal(ev[9].data.total_cost_usd, 0.02);
  assert.equal(ev[10].data.cost_usd, undefined, "no cost: the daemon prices it");
  assert.deepEqual(ev[11].data, { reason: "input", message: "Deploy to staging?" });

  // The subagent's events are its own actor's; the cost report stays at the root.
  const child = ev[13].data.child_session_id;
  assert.match(child, new RegExp(`^${ev[0].session_id}/sub:[0-9a-f]+$`));
  assert.equal(ev[13].data.agent_type, "reviewer");
  for (const e of [ev[14], ev[15], ev[16]]) {
    assert.equal(e.actor_id, child);
    assert.equal(e.parent_actor, ev[0].session_id);
    assert.equal(e.actor_type, "reviewer");
  }
  assert.equal(ev[17].actor_id, undefined);
  assert.ok(Math.abs(ev[17].data.total_cost_usd - 0.03) < 1e-9);
  assert.equal(ev[18].actor_id, undefined);
  assert.deepEqual(ev[18].data, { child_session_id: child, agent_type: "reviewer", status: "done" });
  assert.deepEqual(ev[19].data, { status: "ok" });
  assert.deepEqual(ev[20].data, { status: "ok" });
});

test("an error ends the turn as failed", async () => {
  const { c } = client();
  const s = c.session({ id: "run-7" });
  s.end("error", "out of retries");
  await c.close();
  assert.equal(daemon.events[0].session_id, "test-bot:run-7");
  assert.deepEqual(
    daemon.events.slice(1).map((e) => [e.kind, e.data]),
    [["turn.end", { status: "error", error: "out of retries" }], ["session.end", { status: "error" }]],
  );
});

test("batches by size", async () => {
  const { c } = client({ batchSize: 100 });
  const s = c.session();
  for (let i = 0; i < 249; i++) s.shell(`echo ${i}`, 0);
  await c.close();
  assert.deepEqual(daemon.requests.map((r) => r.length), [100, 100, 50]);
});

test("flushes on the interval", async () => {
  const { c } = client({ flushIntervalMs: 20 });
  c.session();
  for (let i = 0; i < 50 && daemon.events.length === 0; i++) await new Promise((r) => setTimeout(r, 10));
  assert.equal(daemon.events.length, 1);
  await c.close();
});

test("keeps events while the daemon is down and resends the same keys", async () => {
  const { c, warnings } = client({ maxQueue: 100, batchSize: 100 });
  daemon.status = 503;
  const s = c.session();
  for (let i = 0; i < 149; i++) s.shell(`echo ${i}`, 0);
  await c.flush();
  assert.equal(c.stats.queued, 100);
  assert.equal(c.stats.dropped, 50);
  assert.equal(warnings.length, 2, warnings.join("\n")); // one per kind, not per attempt
  daemon.status = 200;
  await c.close();
  assert.equal(daemon.events.length, 100);
  assert.equal(daemon.events.at(-1).data.command, "echo 148", "the oldest were dropped");
  assert.equal(c.stats.queued, 0);
});

test("retries are idempotent", async () => {
  const { c } = client();
  const s = c.session();
  s.turn("hello");
  daemon.status = "lost"; // stored, but the client sees a failure
  await c.flush();
  assert.equal(c.stats.queued, 2);
  daemon.status = 200;
  await c.close();
  assert.deepEqual(daemon.requests[0].map((e) => e.id), daemon.requests[1].map((e) => e.id));
  assert.equal(daemon.keys.size, 2);
  assert.equal(c.stats.sent, 0);
  assert.equal(c.stats.duplicates, 2);
});

test("unreachable daemon: no throw, one warning, bounded queue", async () => {
  const warnings = [];
  const c = new Shiplino({ agent: "test-bot", url: "http://127.0.0.1:1", token: TOKEN, maxQueue: 100, flushIntervalMs: 60_000, onWarning: (m) => warnings.push(m) });
  const s = c.session();
  for (let i = 0; i < 300; i++) s.turn(`p${i}`);
  await c.flush();
  await c.flush();
  assert.equal(c.stats.queued, 100);
  assert.ok(c.stats.dropped >= 200);
  assert.match(c.stats.lastError, /not reachable/);
  assert.equal(warnings.filter((w) => w.includes("not reachable")).length, 1);
  await c.close();
});

test("a bad token drops the batch instead of retrying forever", async () => {
  const { c, warnings } = client({ token: "wrong" });
  c.session();
  await c.close();
  assert.equal(c.stats.queued, 0);
  assert.equal(c.stats.rejected, 1);
  assert.match(warnings[0], /401/);
});

test("finds the token and port in the Shiplino home", async () => {
  const home = mkdtempSync(join(tmpdir(), "shiplino-sdk-"));
  try {
    writeFileSync(join(home, "token"), `${TOKEN}\n`);
    writeFileSync(join(home, "port"), `${server.address().port}\n`);
    const c = new Shiplino({ agent: "test-bot", home, flushIntervalMs: 60_000 });
    c.session();
    await c.close();
    assert.equal(daemon.events.length, 1);
  } finally {
    rmSync(home, { recursive: true, force: true });
  }
});

test("not installed: a silent no-op with one warning", async () => {
  const home = mkdtempSync(join(tmpdir(), "shiplino-sdk-"));
  try {
    const warnings = [];
    const c = new Shiplino({ agent: "test-bot", home, onWarning: (m) => warnings.push(m) });
    const s = c.session();
    s.tool("Read", {}).end();
    s.subagent("x").end();
    s.end();
    await c.close();
    assert.equal(c.enabled, false);
    assert.equal(warnings.length, 1);
    assert.match(warnings[0], /no API token/);
  } finally {
    rmSync(home, { recursive: true, force: true });
  }
});

test("an invalid agent name disables the client instead of throwing", async () => {
  const warnings = [];
  for (const agent of ["has space", "a:b", "a/b", "", "x".repeat(65), undefined]) {
    const c = new Shiplino({ agent, url, token: TOKEN, onWarning: (m) => warnings.push(m) });
    c.session().end();
    await c.close();
    assert.equal(c.enabled, false);
  }
  assert.equal(warnings.length, 6);
  assert.equal(daemon.events.length, 0);
});

test("a throwing warning handler doesn't break the agent", () => {
  const c = new Shiplino({ agent: "a b", onWarning: () => { throw new Error("boom"); } });
  assert.equal(c.enabled, false);
});

test("tool kinds", () => {
  assert.equal(toolKind("Bash"), "shell");
  assert.equal(toolKind("edit"), "edit");
  assert.equal(toolKind("WebFetch"), "web");
  assert.equal(toolKind("mcp__github__create_issue"), "mcp");
  assert.equal(toolKind("deploy"), "other");
});

test("flushes at exit without close()", async () => {
  const dist = fileURLToPath(new URL("../dist/index.js", import.meta.url));
  const script = `
    import { Shiplino } from ${JSON.stringify(dist)};
    const c = new Shiplino({ agent: "exit-bot", url: process.argv[1], token: process.argv[2] });
    c.session({ title: "short run" }).end();
  `;
  const run = (target) =>
    new Promise((resolve) => {
      const child = spawn(process.execPath, ["--input-type=module", "-e", script, target, TOKEN], { stdio: ["ignore", "ignore", "pipe"] });
      let stderr = "";
      child.stderr.on("data", (d) => {
        stderr += d;
      });
      const timer = setTimeout(() => child.kill(), 15_000);
      child.on("exit", (code) => {
        clearTimeout(timer);
        resolve({ code, stderr });
      });
    });

  const ok = await run(url);
  assert.equal(ok.code, 0, ok.stderr);
  assert.deepEqual(daemon.events.map((e) => e.kind), ["session.start", "session.end"]);

  // A daemon that's down must not keep the process alive.
  const down = await run("http://127.0.0.1:1");
  assert.equal(down.code, 0, down.stderr);
  assert.match(down.stderr, /not reachable/);
});
