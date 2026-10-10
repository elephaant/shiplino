// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

import { defineConfig, devices } from "@playwright/test";

const ci = !!process.env.CI;

export default defineConfig({
  testDir: "./tests",
  globalSetup: "./global-setup.ts",
  // One daemon is shared by every test, so run them one at a time.
  workers: 1,
  fullyParallel: false,
  forbidOnly: ci,
  // No retries: a flaky test is a bug to fix, not to hide.
  retries: 0,
  reporter: ci ? [["list"], ["html", { open: "never" }]] : [["list"]],
  expect: { timeout: 10_000 },
  use: {
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  projects: [
    {
      name: "chromium",
      // Wide enough for every board column without scrolling.
      use: { ...devices["Desktop Chrome"], viewport: { width: 1600, height: 900 } },
    },
  ],
});
