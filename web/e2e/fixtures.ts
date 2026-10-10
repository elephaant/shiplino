// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

import path from "node:path";
import { test as base, expect, type Locator, type Page } from "@playwright/test";
import { env } from "./shiplino";

/**
 * test points every page at the test daemon and fails the test on any
 * console error or uncaught page error.
 */
export const test = base.extend({
  // biome-ignore lint/correctness/noEmptyPattern: Playwright fixtures take an object pattern
  baseURL: async ({}, use) => use(env.url),
  page: async ({ page }, use) => {
    const errors: string[] = [];
    page.on("console", (m) => {
      if (m.type() === "error") errors.push(`console: ${m.text()}`);
    });
    page.on("pageerror", (e) => errors.push(`page: ${e.message}`));
    await use(page);
    expect(errors, "console or page errors").toEqual([]);
  },
});

export { expect };

/** boardURL is the board of a seeded project (detected as a plain folder). */
export const boardURL = (project: string) =>
  `/board/?project=${encodeURIComponent(`dir:${path.join(env.work, project).replaceAll("\\", "/")}`)}`;

/** column is a board column by its name. */
export const column = (page: Page, name: string) => page.getByRole("region", { name, exact: true });

/** dropZone is the card list of a column, where cards are dropped. */
export const dropZone = (page: Page, name: string) => column(page, name).locator(":scope > div");

/** card is a board card (its draggable wrapper) by its title. */
export const card = (scope: Page | Locator, title: string) =>
  scope.locator('[aria-roledescription="draggable"]').filter({ hasText: title });

/**
 * drag moves source onto target with real pointer events: press, a few
 * small steps to pass the drag threshold, then many steps to the target,
 * like a hand would. ready runs before the release, to wait for the board
 * to register the drop target.
 */
export async function drag(page: Page, source: Locator, target: Locator, ready: () => Promise<void>) {
  const from = await source.boundingBox();
  const to = await target.boundingBox();
  if (!from || !to) throw new Error("drag: source or target is not visible");
  const x = from.x + from.width / 2;
  const y = from.y + from.height / 2;
  await page.mouse.move(x, y);
  await page.mouse.down();
  await page.mouse.move(x + 12, y + 12, { steps: 4 });
  await page.mouse.move(to.x + to.width / 2, to.y + to.height / 2, { steps: 25 });
  await ready();
  await page.mouse.up();
  // Wait for the drop animation to end: no new drag can start before.
  await expect(page.locator("[data-dnd-dragging], [data-dnd-dropping], [data-dnd-overlay]:not(:empty)")).toHaveCount(0);
}
