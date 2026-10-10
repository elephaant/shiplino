import { expect, test } from "../fixtures";
import { seed } from "../shiplino";

test("overview lists the projects and what needs you", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "All projects" })).toBeVisible();
  const table = page.getByRole("table");
  for (const name of ["demo-app", "api-server"]) {
    await expect(table.getByRole("link", { name, exact: true })).toBeVisible();
  }

  const needsYou = page.locator("#needs-you");
  await expect(needsYou.getByText(/Needs you \(\d+\)/)).toBeVisible();
  await expect(needsYou.getByRole("link", { name: new RegExp(seed.waiting.prompt) })).toBeVisible();
  await expect(needsYou.getByText(seed.running.prompt)).toHaveCount(0);

  // The header's live strip counts the waiting session too.
  await expect(page.getByRole("link", { name: /\d+ running, [1-9]\d* waiting on you/ })).toBeVisible();
});

test("timeline, insights and settings render", async ({ page }) => {
  for (const [url, heading] of [
    ["/timeline/", "Timeline"],
    ["/insights/", "Insights"],
    ["/settings/", "Settings"],
  ] as const) {
    await page.goto(url);
    await expect(page.getByRole("heading", { level: 1, name: heading })).toBeVisible();
  }
  await page.goto("/timeline/");
  await expect(page.getByText(seed.review.prompt).first()).toBeVisible();
});

test("the theme toggle switches between light and dark", async ({ page }) => {
  await page.goto("/");
  const html = page.locator("html");
  const toggle = page.getByRole("button", { name: /^Theme: / });
  // system → light → dark → system
  await expect(toggle).toHaveAccessibleName(/Theme: system/);
  await toggle.click();
  await expect(html).toHaveClass(/\blight\b/);
  await expect(html).not.toHaveClass(/\bdark\b/);
  await toggle.click();
  await expect(html).toHaveClass(/\bdark\b/);
  // The choice survives a reload.
  await page.reload();
  await expect(html).toHaveClass(/\bdark\b/);
  await page.getByRole("button", { name: /^Theme: / }).click();
  await expect(page.getByRole("button", { name: /^Theme: / })).toHaveAccessibleName(/Theme: system/);
});
