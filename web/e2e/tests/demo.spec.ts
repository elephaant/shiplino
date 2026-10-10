// `shiplino demo`: its own daemon with synthetic data, separate from the
// test daemon of the other specs.

import { type ChildProcessWithoutNullStreams, spawn } from "node:child_process";
import { existsSync, mkdtempSync, readdirSync, rmSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { card, column, expect, test } from "../fixtures";
import { bin, daemonEnv } from "../shiplino";

let demo: ChildProcessWithoutNullStreams;
let home: string;
let url: string;
let output = "";

test.beforeAll(async () => {
  home = mkdtempSync(path.join(os.tmpdir(), "shiplino-e2e-demo-"));
  demo = spawn(bin, ["demo", "--no-open"], { env: daemonEnv(home) });
  demo.stdout.on("data", (b) => {
    output += b;
  });
  demo.stderr.on("data", (b) => {
    output += b;
  });
  const deadline = Date.now() + 30_000;
  while (!url) {
    const m = output.match(/Shiplino demo: (http:\/\/localhost:\d+)/);
    if (m?.[1]) url = m[1];
    else if (demo.exitCode !== null || Date.now() > deadline) throw new Error(`the demo didn't start:\n${output}`);
    else await new Promise((r) => setTimeout(r, 100));
  }
});

test.afterAll(async () => {
  if (demo.exitCode === null) {
    const exited = new Promise((r) => demo.once("exit", r));
    demo.kill("SIGINT"); // like Ctrl-C
    await Promise.race([exited, new Promise((r) => setTimeout(r, 10_000))]);
  }
  expect(output).toContain("its data was deleted");
  // The demo used its own temporary folder, never this HOME.
  expect(readdirSync(home)).toEqual([]);
  if (existsSync(home)) rmSync(home, { recursive: true, force: true });
});

test("the demo shows a banner and seeded projects", async ({ page }) => {
  await page.goto(`${url}/`);
  await expect(page.getByRole("status").filter({ hasText: "Demo data." })).toBeVisible();
  const table = page.getByRole("table");
  for (const name of ["storefront", "billing-api", "mobile-app", "docs-site"]) {
    await expect(table.getByRole("link", { name, exact: true })).toBeVisible();
  }
  await expect(page.locator("#needs-you").getByText(/Needs you \([1-9]\d*\)/)).toBeVisible();
});

test("the demo board is seeded and live", async ({ page }) => {
  // The live session is running most of the time; between tasks it spends
  // a few seconds in Review and Done.
  test.setTimeout(120_000);
  await page.goto(`${url}/board/?project=${encodeURIComponent("example.com/acme/storefront")}`);
  await expect(card(column(page, "Backlog"), "Gift cards at checkout")).toBeVisible();
  await expect(card(column(page, "Review"), "Show the estimated delivery date")).toBeVisible();
  await expect(column(page, "Running").locator('[aria-roledescription="draggable"]').first()).toBeVisible({
    timeout: 90_000,
  });
});
