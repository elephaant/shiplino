import { expect, test } from "../fixtures";
import { seed } from "../shiplino";

test("Ctrl K search finds a prompt and Enter opens the session", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "All projects" })).toBeVisible();
  await page.keyboard.press("ControlOrMeta+K");
  const dialog = page.getByRole("dialog", { name: "Search" });
  await dialog.getByRole("combobox").fill("greeting helper");
  const hit = dialog.getByRole("option", { name: /greeting helper/ });
  await expect(hit).toBeVisible();
  await expect(hit).toHaveAttribute("data-selected", "true");
  await page.keyboard.press("Enter");

  await expect(page).toHaveURL(new RegExp(`/session/?\\?id=claude-code%3A${seed.review.id}$`));
  await expect(page.getByRole("heading", { level: 1, name: seed.review.prompt })).toBeVisible();
});

test("the session timeline lists tool calls and Changes shows the files", async ({ page }) => {
  await page.goto(`/session/?id=${encodeURIComponent(`claude-code:${seed.review.id}`)}`);
  await expect(page.getByRole("heading", { level: 1, name: seed.review.prompt })).toBeVisible();

  const timeline = page.getByRole("tabpanel", { name: "Timeline" });
  await expect(timeline.getByText(seed.review.prompt)).toBeVisible();
  await expect(timeline.getByText("Read src/greet.ts")).toBeVisible();
  await expect(timeline.getByText(/Edited src\/greet\.ts\s+\+1 −1/)).toBeVisible();
  await expect(timeline.getByText("npm test -- greet")).toBeVisible();

  await page.getByRole("tab", { name: /Changes \(1\)/ }).click();
  const changes = page.getByRole("tabpanel", { name: /Changes/ });
  await expect(changes.getByText("src/greet.ts").first()).toBeVisible();
});
