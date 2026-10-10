// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

// Against a real daemon: set SHIPLINO_BIN to a built `shiplino` binary
// (go build -o bin/shiplino ./cmd/shiplino). It runs with a temp home.

import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

import { Shiplino } from "../dist/index.js";

const bin = process.env.SHIPLINO_BIN;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function waitFor(f, what) {
  for (let i = 0; i < 200; i++) {
    const v = await f().catch(() => undefined);
    if (v) return v;
    await sleep(50);
  }
  throw new Error(`timed out waiting for ${what}`);
}

test("a session reaches the real daemon", { skip: !bin && "set SHIPLINO_BIN to run" }, async () => {
  const home = mkdtempSync(join(tmpdir(), "shiplino-sdk-e2e-"));
  const env = { ...process.env, SHIPLINO_HOME: join(home, ".shiplino"), HOME: home, USERPROFILE: home };
  const daemon = spawn(bin, ["daemon"], { env, stdio: ["ignore", "ignore", "pipe"] });
  let stderr = "";
  daemon.stderr.on("data", (d) => {
    stderr += d;
  });
  try {
    const shome = env.SHIPLINO_HOME;
    const port = await waitFor(async () => readFileSync(join(shome, "port"), "utf8").trim(), `the daemon to listen\n${stderr}`);
    const token = readFileSync(join(shome, "token"), "utf8").trim();
    const base = `http://127.0.0.1:${port}`;
    await waitFor(async () => (await fetch(`${base}/api/v1/health`)).ok, "health");

    const shiplino = new Shiplino({ agent: "sdk-e2e", home: shome });
    const s = shiplino.session({ title: "SDK end to end", cwd: home });
    s.turn("Check the SDK");
    s.tool("Read", { file_path: join(home, "a.txt") }).end();
    s.fileEdit(join(home, "a.txt"), 3, 1);
    s.usage({ model: "claude-opus-5-5", inputTokens: 1000, outputTokens: 100, costUsd: 0.05 });
    const sub = s.subagent("reviewer");
    sub.usage({ model: "claude-opus-5-5", inputTokens: 10, outputTokens: 10, costUsd: 0.01 });
    sub.end();
    s.end();
    // Without a cost from the agent, the daemon prices the tokens.
    const s2 = shiplino.session({ title: "Priced by the daemon", cwd: home });
    s2.usage({ model: "claude-opus-5-5", inputTokens: 1000, outputTokens: 100 });
    s2.end();
    await shiplino.close();
    assert.equal(shiplino.stats.rejected, 0, shiplino.stats.lastError);
    assert.ok(shiplino.stats.sent > 0);

    const get = async (path) => {
      const r = await fetch(`${base}${path}`, { headers: { authorization: `Bearer ${token}` } });
      return r.json();
    };
    const root = s.sessionId;
    const session = await waitFor(async () => {
      const { sessions } = await get("/api/v1/sessions");
      const found = sessions.find((x) => x.id === root);
      return found && found.status !== "running" ? found : undefined;
    }, "the session");
    assert.equal(session.agent, "sdk-e2e");
    assert.equal(session.title, "SDK end to end");
    assert.equal(session.status, "review"); // a file was edited
    assert.equal(session.lines_added, 3);
    assert.equal(session.input_tokens, 1000);
    assert.equal(session.cost_source, "reported");
    assert.ok(Math.abs(session.best_cost_usd - 0.06) < 1e-9, String(session.best_cost_usd));

    const child = await get(`/api/v1/sessions/${encodeURIComponent(sub.id)}`);
    assert.equal(child.parent_id, root);
    assert.equal(child.actor_type, "reviewer");
    assert.equal(child.status, "done");

    const priced = await get(`/api/v1/sessions/${encodeURIComponent(s2.sessionId)}`);
    assert.equal(priced.cost_source, "computed");
    assert.ok(priced.best_cost_usd > 0);
  } finally {
    daemon.kill();
    await new Promise((r) => daemon.once("exit", r));
    rmSync(home, { recursive: true, force: true });
  }
});
