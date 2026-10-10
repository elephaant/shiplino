// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

import path from "node:path";
import { boardURL, card, column, drag, dropZone, expect, test } from "../fixtures";
import { claude, env, hook, seed } from "../shiplino";

test("dragging a Review card to Done pins it", async ({ page }) => {
  await page.goto(boardURL(seed.review.project));
  const source = card(column(page, "Review"), seed.review.prompt);
  await expect(source).toBeVisible();

  const done = dropZone(page, "Done");
  await drag(page, source, done, async () => {
    // The held card's placeholder has moved into Done.
    await expect(card(done, seed.review.prompt)).toBeAttached();
  });

  await expect(card(done, seed.review.prompt).getByLabel("Pinned")).toBeVisible();
  await expect(card(column(page, "Review"), seed.review.prompt)).toHaveCount(0);

  // The move is stored: it survives a reload.
  await page.reload();
  await expect(card(column(page, "Done"), seed.review.prompt).getByLabel("Pinned")).toBeVisible();
});

test("dropping an auto card on Running or Waiting is refused", async ({ page }) => {
  await page.goto(boardURL(seed.refuse.project));
  const toast = page.getByText("Running and Waiting follow the agent");
  for (const target of ["Running", "Waiting on you"]) {
    const source = card(column(page, "Review"), seed.refuse.prompt);
    await expect(source).toBeVisible();
    const zone = dropZone(page, target);
    await drag(page, source, zone, async () => {
      // The column shows it won't take the card while it's held there.
      await expect(zone).toHaveAttribute("data-blocked", "true");
    });
    await expect(zone).not.toHaveAttribute("data-blocked");
    await expect(toast.first()).toBeVisible();
    // The card is back where it was, and not pinned.
    await expect(card(column(page, "Review"), seed.refuse.prompt)).toBeVisible();
    await expect(card(column(page, target), seed.refuse.prompt)).toHaveCount(0);
    await expect(card(column(page, "Review"), seed.refuse.prompt).getByLabel("Pinned")).toHaveCount(0);
  }
  await page.reload();
  await expect(card(column(page, "Review"), seed.refuse.prompt)).toBeVisible();
});

test("a new hook event adds a card live, and a status change moves it", async ({ page }) => {
  await page.goto(boardURL("demo-app"));
  await expect(card(column(page, "Review"), seed.refuse.prompt)).toBeVisible();

  const prompt = "Write the release notes draft";
  const ev = claude("e2e-live", path.join(env.work, "demo-app"));
  hook(env.home, ev("SessionStart", { source: "startup" }));
  hook(env.home, ev("UserPromptSubmit", { prompt_id: "p-1", prompt }));
  await expect(card(column(page, "Running"), prompt)).toBeVisible();

  hook(env.home, ev("Notification", { message: "Claude is waiting for your input", notification_type: "idle_prompt" }));
  await expect(card(column(page, "Waiting on you"), prompt)).toBeVisible();
  await expect(card(column(page, "Running"), prompt)).toHaveCount(0);

  // A finished turn without edits goes straight to Done.
  hook(env.home, ev("Stop", { prompt_id: "p-1", last_assistant_message: "Drafted the notes." }));
  await expect(card(column(page, "Done"), prompt)).toBeVisible();
  await expect(card(column(page, "Waiting on you"), prompt)).toHaveCount(0);
});
