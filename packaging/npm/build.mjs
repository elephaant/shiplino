// Builds the npm packages for one release from its GitHub release assets:
// the `shiplino` package (a small launcher) and one package per platform
// holding that platform's binary, unchanged.
//
//   node packaging/npm/build.mjs --version 0.1.0 --assets <dir> --out <dir>
//
// <dir> holds the release archives and checksums.txt (the publish workflow
// verifies the checksums file's Sigstore signature first). Every archive
// must match its SHA-256 in checksums.txt, or nothing is built.
import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { chmodSync, cpSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { parseArgs } from "node:util";

const here = dirname(fileURLToPath(import.meta.url));
const repo = join(here, "..", "..");

// npm's os/cpu names → GoReleaser's archive names.
const targets = [
  { os: "darwin", cpu: "arm64", goos: "darwin", goarch: "arm64" },
  { os: "darwin", cpu: "x64", goos: "darwin", goarch: "amd64" },
  { os: "linux", cpu: "arm64", goos: "linux", goarch: "arm64" },
  { os: "linux", cpu: "x64", goos: "linux", goarch: "amd64" },
  { os: "win32", cpu: "arm64", goos: "windows", goarch: "arm64" },
  { os: "win32", cpu: "x64", goos: "windows", goarch: "amd64" },
];

const { values } = parseArgs({
  options: { version: { type: "string" }, assets: { type: "string" }, out: { type: "string" } },
});
const version = (values.version ?? "").replace(/^v/, "");
if (!/^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/.test(version) || !values.assets || !values.out) {
  console.error("usage: build.mjs --version X.Y.Z[-pre] --assets <dir> --out <dir>");
  process.exit(2);
}

const sums = new Map();
for (const line of readFileSync(join(values.assets, "checksums.txt"), "utf8").split("\n")) {
  const m = line.match(/^([0-9a-f]{64}) [ *]?(\S+)$/);
  if (m) sums.set(m[2], m[1]);
}

const base = {
  version,
  license: "Apache-2.0",
  homepage: "https://github.com/elephaant/shiplino",
  repository: { type: "git", url: "git+https://github.com/elephaant/shiplino.git" },
};

rmSync(values.out, { recursive: true, force: true });
const tmp = mkdtempSync(join(tmpdir(), "shiplino-npm-"));
const optional = {};
try {
  for (const t of targets) {
    const exe = t.os === "win32" ? "shiplino.exe" : "shiplino";
    const archive = `shiplino_${version}_${t.goos}_${t.goarch}.${t.os === "win32" ? "zip" : "tar.gz"}`;
    const file = join(values.assets, archive);
    const want = sums.get(archive);
    if (!want) throw new Error(`${archive} isn't listed in checksums.txt`);
    const got = createHash("sha256").update(readFileSync(file)).digest("hex");
    if (got !== want) throw new Error(`${archive}: SHA-256 ${got}, checksums.txt says ${want}`);

    // Extract only the binary, by its exact name.
    const x = join(tmp, `${t.os}-${t.cpu}`);
    mkdirSync(x);
    if (archive.endsWith(".zip")) execFileSync("unzip", ["-q", file, exe, "-d", x]);
    else execFileSync("tar", ["-xzf", file, "-C", x, exe]);

    const name = `@shiplino/${t.os}-${t.cpu}`;
    const dir = join(values.out, name);
    mkdirSync(join(dir, "bin"), { recursive: true });
    cpSync(join(x, exe), join(dir, "bin", exe));
    chmodSync(join(dir, "bin", exe), 0o755);
    cpSync(join(repo, "LICENSE"), join(dir, "LICENSE"));
    writeFileSync(
      join(dir, "README.md"),
      `# ${name}\n\nThe Shiplino binary for ${t.os} ${t.cpu}. Install [\`shiplino\`](https://www.npmjs.com/package/shiplino) instead; it picks this package for you.\n`,
    );
    const pkg = {
      name,
      ...base,
      description: `The Shiplino binary for ${t.os} ${t.cpu}`,
      os: [t.os],
      cpu: [t.cpu],
      files: ["bin"],
      preferUnplugged: true,
    };
    writeFileSync(join(dir, "package.json"), `${JSON.stringify(pkg, null, 2)}\n`);
    optional[name] = version;
    console.log(`${name}: ${archive} verified`);
  }

  const dir = join(values.out, "shiplino");
  cpSync(join(here, "shiplino"), dir, { recursive: true });
  cpSync(join(repo, "LICENSE"), join(dir, "LICENSE"));
  const pkg = JSON.parse(readFileSync(join(dir, "package.json"), "utf8"));
  pkg.version = version;
  pkg.optionalDependencies = optional;
  writeFileSync(join(dir, "package.json"), `${JSON.stringify(pkg, null, 2)}\n`);
  console.log(`shiplino ${version}: ${Object.keys(optional).length} platform packages`);
} finally {
  rmSync(tmp, { recursive: true, force: true });
}
