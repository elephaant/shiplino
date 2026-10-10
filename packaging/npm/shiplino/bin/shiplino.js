#!/usr/bin/env node
// Runs the prebuilt Shiplino binary from the platform package npm installed
// next to this one (an optional dependency, like esbuild and Biome do).
// Nothing is downloaded or run at install time.
"use strict";

const { spawnSync } = require("node:child_process");

const key = `${process.platform}-${process.arch}`;
const exe = process.platform === "win32" ? "shiplino.exe" : "shiplino";

let bin;
try {
  bin = require.resolve(`@shiplino/${key}/bin/${exe}`);
} catch {
  console.error(
    `shiplino: the binary for ${key} isn't installed.\n` +
      "Supported: darwin, linux and win32 on x64 and arm64. If yours is one of them, reinstall\n" +
      "without --omit=optional / --no-optional, or use the installer:\n" +
      "  https://github.com/elephaant/shiplino#install",
  );
  process.exit(1);
}

const r = spawnSync(bin, process.argv.slice(2), { stdio: "inherit" });
if (r.error) {
  console.error(`shiplino: cannot run ${bin}: ${r.error.message}`);
  process.exit(1);
}
if (r.signal) {
  process.kill(process.pid, r.signal);
}
process.exit(r.status ?? 1);
