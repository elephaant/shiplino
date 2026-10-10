// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2
//
// Runs the plugin's event handler against a fake `shiplino` binary.
// Run with `node --test plugins/opencode/` (go test ./plugins/opencode does
// it too). UPDATE=1 rewrites the adapter fixture hooks.jsonl from bus.jsonl.

import assert from "node:assert/strict"
import { chmodSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { dirname, join } from "node:path"
import { test } from "node:test"
import { fileURLToPath, pathToFileURL } from "node:url"

const here = dirname(fileURLToPath(import.meta.url))
const fixtures = join(here, "..", "..", "pkg", "adapters", "opencode", "testdata", "v1.18")
const unix = process.platform !== "win32"

// load copies the plugin with bin filled in, the way `shiplino setup` does.
async function load(dir, bin) {
  const src = readFileSync(join(here, "shiplino.js"), "utf8")
  assert.ok(src.includes('"__SHIPLINO_BIN__"'))
  const file = join(dir, "shiplino.mjs")
  writeFileSync(file, src.replace('"__SHIPLINO_BIN__"', JSON.stringify(bin)))
  return import(pathToFileURL(file).href)
}

async function waitFor(fn, ms = 10000) {
  const end = Date.now() + ms
  for (;;) {
    if (fn()) return true
    if (Date.now() > end) return false
    await new Promise((r) => setTimeout(r, 25))
  }
}

test("exports one plugin function, as OpenCode's loader requires", async () => {
  const dir = mkdtempSync(join(tmpdir(), "shiplino-oc-"))
  try {
    const mod = await load(dir, "/nonexistent/shiplino")
    assert.deepEqual(Object.keys(mod), ["ShiplinoPlugin"])
    const hooks = await mod.ShiplinoPlugin({ directory: "/home/dev/shop" })
    assert.deepEqual(Object.keys(hooks), ["event"])
  } finally {
    rmSync(dir, { recursive: true, force: true })
  }
})

test("sends each recorded event to `shiplino hook --agent opencode`", { skip: !unix && "needs a shell script binary" }, async () => {
  const dir = mkdtempSync(join(tmpdir(), "shiplino-oc-"))
  const out = join(dir, "out")
  const bin = join(dir, "fake-shiplino")
  writeFileSync(bin, `#!/bin/sh\nmkdir -p "${out}"\necho "$@" > "${out}/$$.args"\ncat > "${out}/$$.tmp" && mv "${out}/$$.tmp" "${out}/$$.json"\n`)
  chmodSync(bin, 0o755)
  const realNow = Date.now
  try {
    const mod = await load(dir, bin)
    const hooks = await mod.ShiplinoPlugin({ directory: "/home/dev/shop" })
    const events = readFileSync(join(fixtures, "bus.jsonl"), "utf8").trim().split("\n").map((l) => JSON.parse(l))
    let clock = 1791626400000
    Date.now = () => (clock += 1000)
    for (const event of events) assert.equal(await hooks.event({ event }), undefined)
    Date.now = realNow

    const want = readFileSync(join(fixtures, "hooks.jsonl"), "utf8").trim().split("\n")
    const count = () => readdirSync(out).filter((f) => f.endsWith(".json")).length
    const n = process.env.UPDATE ? 0 : want.length
    await waitFor(() => {
      try {
        return count() >= n
      } catch {
        return false
      }
    })
    if (process.env.UPDATE) await new Promise((r) => setTimeout(r, 2000))
    const files = readdirSync(out)
    for (const f of files.filter((f) => f.endsWith(".args"))) {
      assert.equal(readFileSync(join(out, f), "utf8").trim(), "hook --agent opencode")
    }
    const got = files
      .filter((f) => f.endsWith(".json"))
      .map((f) => JSON.parse(readFileSync(join(out, f), "utf8")))
      .sort((a, b) => a.timestamp - b.timestamp)
      .map((p) => JSON.stringify(p))
    if (process.env.UPDATE) {
      writeFileSync(join(fixtures, "hooks.jsonl"), got.join("\n") + "\n")
      return
    }
    assert.deepEqual(got, want)
  } finally {
    Date.now = realNow
    rmSync(dir, { recursive: true, force: true })
  }
})

test("swallows every error and prints nothing", async () => {
  // Nothing in the plugin can print: no console, no process output streams.
  const src = readFileSync(join(here, "shiplino.js"), "utf8")
  assert.doesNotMatch(src, /console\.|process\.std(out|err)/)

  const dir = mkdtempSync(join(tmpdir(), "shiplino-oc-"))
  const rejections = []
  const onRejection = (e) => rejections.push(e)
  process.on("unhandledRejection", onRejection)
  try {
    const mod = await load(dir, join(dir, "missing", "shiplino"))
    const hooks = await mod.ShiplinoPlugin(undefined)
    const hostile = {}
    Object.defineProperty(hostile, "info", {
      get: () => {
        throw new Error("boom")
      },
    })
    const inputs = [
      undefined,
      null,
      {},
      { event: null },
      { event: { type: 42 } },
      { event: { type: "session.created" } },
      { event: { type: "session.created", properties: hostile } },
      { event: { type: "message.part.updated", properties: { part: { id: "p", type: "tool", state: null } } } },
      { event: { type: "session.idle", properties: { sessionID: "ses_x" } } }, // spawns the missing binary
      { event: { type: "permission.asked", properties: { sessionID: "ses_x", id: "per_x", pattern: "x" } } },
    ]
    for (const input of inputs) assert.equal(await hooks.event(input), undefined)
    await new Promise((r) => setTimeout(r, 300))
  } finally {
    process.off("unhandledRejection", onRejection)
    rmSync(dir, { recursive: true, force: true })
  }
  assert.deepEqual(rejections, [])
})
