import { readFileSync } from "node:fs";
import type { Page } from "@playwright/test";
import { expect, test } from "../fixtures";
import { env } from "../shiplino";

/** Width and height from a PNG's IHDR chunk. */
function pngSize(file: string): [number, number] {
  const b = readFileSync(file);
  expect(b.subarray(1, 4).toString()).toBe("PNG");
  return [b.readUInt32BE(16), b.readUInt32BE(20)];
}

/** Brightness (0-255) of the preview's top-left pixel: the card's background. */
const cornerBrightness = (page: Page) =>
  page
    .getByRole("dialog")
    .locator("canvas")
    .evaluate((c: HTMLCanvasElement) => {
      const [r, g, b] = c.getContext("2d")!.getImageData(4, 4, 1, 1).data;
      return (r! + g! + b!) / 3;
    });

test("share week renders a PNG in both themes, in the browser", async ({ page }, info) => {
  const uploads: string[] = [];
  page.on("request", (r) => {
    // Reads and prefetches are GET/HEAD to the daemon; anything else would be an upload.
    const local = r.url().startsWith(new URL(env.url).origin) || r.url().startsWith("blob:");
    if (!local || !["GET", "HEAD"].includes(r.method())) uploads.push(`${r.method()} ${r.url()}`);
  });
  await page.goto("/insights/");
  await page.getByRole("button", { name: "Share week" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("checkbox", { name: "Include project names" })).not.toBeChecked();
  const img = dialog.getByRole("img", { name: /^Week summary/ });
  await expect(img).toBeVisible();

  const sizes: Record<string, number> = {};
  for (const mode of ["Light", "Dark"] as const) {
    await dialog.getByRole("radio", { name: mode }).click();
    await expect(dialog.getByRole("radio", { name: mode })).toHaveAttribute("aria-checked", "true");
    await expect(img).toHaveJSProperty("width", 1200);
    await expect(img).toHaveJSProperty("height", 630);
    sizes[mode] = await cornerBrightness(page);
    const [download] = await Promise.all([
      page.waitForEvent("download"),
      dialog.getByRole("button", { name: "Download PNG" }).click(),
    ]);
    expect(download.suggestedFilename()).toMatch(/^shiplino-week-\d{4}-\d{2}-\d{2}\.png$/);
    const file = info.outputPath(`share-week-${mode.toLowerCase()}.png`);
    await download.saveAs(file);
    expect(pngSize(file)).toEqual([1200, 630]);
    await dialog.screenshot({ path: info.outputPath(`dialog-${mode.toLowerCase()}.png`) });
  }
  expect(sizes.Light!).toBeGreaterThan(sizes.Dark!);
  // Rendering and downloading send nothing anywhere.
  expect(uploads).toEqual([]);
});
