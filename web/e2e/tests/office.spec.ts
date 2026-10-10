import path from "node:path";
import type { Page } from "@playwright/test";
import { expect, test } from "../fixtures";
import { claude, env, hook, seed } from "../shiplino";

const literal = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

/** character is a session's character on the office floor, by its title. */
const character = (page: Page, title: string) =>
  page.getByRole("region", { name: "Office floor" }).getByRole("link", { name: new RegExp(`^${literal(title)} \\(`) });

test("the office seats live sessions and follows them live", async ({ page }, info) => {
  await page.goto("/office/");
  await expect(page.getByRole("heading", { name: "Office" })).toBeVisible();
  await expect(character(page, seed.running.prompt)).toHaveAttribute("data-office-character", "terminal");
  await expect(character(page, seed.api.prompt)).toHaveAttribute("data-office-character", "terminal");
  const waiting = character(page, seed.waiting.prompt);
  await expect(waiting).toHaveAttribute("data-office-character", "waiting");
  await expect(waiting).toHaveAccessibleName(/At the bell: needs approval$/);
  // One room per project, named like the project and linked to its board.
  await expect(page.locator("[data-office-room]")).toHaveText(["api-server", "demo-app"]);

  // A new session walks in, types, then goes to the bell.
  const prompt = "Sketch the office floor plan";
  const ev = claude("e2e-office", path.join(env.work, "demo-app"));
  hook(env.home, ev("SessionStart", { source: "startup" }));
  hook(env.home, ev("UserPromptSubmit", { prompt_id: "p-1", prompt }));
  const file = path.join(env.work, "demo-app", "src", "plan.ts");
  hook(env.home, ev("PreToolUse", { tool_name: "Edit", tool_input: { file_path: file }, tool_use_id: "tu-1" }));
  await expect(character(page, prompt)).toHaveAttribute("data-office-character", "typing");
  hook(
    env.home,
    ev("Notification", { message: "Claude needs your permission", notification_type: "permission_prompt" }),
  );
  await expect(character(page, prompt)).toHaveAttribute("data-office-character", "waiting");

  // Hover shows the card.
  await character(page, prompt).hover();
  await expect(page.getByRole("tooltip")).toContainText(prompt);
  await expect(page.getByRole("tooltip")).toContainText("Claude Code");

  for (const scheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme: scheme });
    await page.mouse.move(0, 0);
    await page.waitForTimeout(scheme === "light" ? 3000 : 300); // let the walk to the bell finish
    await page
      .getByRole("region", { name: "Office floor" })
      .screenshot({ path: info.outputPath(`office-${scheme}.png`) });
  }

  hook(env.home, ev("Stop", { prompt_id: "p-1", last_assistant_message: "Done." }));
  await expect(character(page, prompt)).toHaveAttribute("data-office-character", "celebrating");

  // Clicking a character opens its session.
  await character(page, seed.running.prompt).click();
  await expect(page).toHaveURL(new RegExp(`/session/?\\?id=claude-code%3A${seed.running.id}$`));
  await expect(page.getByRole("heading", { level: 1, name: seed.running.prompt })).toBeVisible();
});

test("reduced motion draws a still office", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.goto("/office/");
  await expect(character(page, seed.waiting.prompt)).toHaveAttribute("data-office-character", "waiting");
  const canvas = page.getByRole("region", { name: "Office floor" }).locator("canvas");
  const frame = () => canvas.evaluate((c: HTMLCanvasElement) => c.toDataURL());
  const first = await frame();
  await page.waitForTimeout(600);
  expect(await frame()).toBe(first);
});
