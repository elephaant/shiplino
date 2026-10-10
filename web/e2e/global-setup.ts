// Starts the real shiplino daemon in a temp HOME, seeds it through the real
// hook shim, and returns the teardown. Tests read the daemon's address from
// SHIPLINO_E2E_URL.

import { type ChildProcess, spawn } from "node:child_process";
import { createWriteStream, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { bin, claude, daemonEnv, hook, seed } from "./shiplino";

const logFile = path.join(__dirname, "daemon.log");

export default async function globalSetup() {
  if (!existsSync(bin)) throw new Error(`${bin} not found: run "make build" first (or set SHIPLINO_BIN)`);

  const root = mkdtempSync(path.join(os.tmpdir(), "shiplino-e2e-"));
  const home = path.join(root, "home");
  const work = path.join(root, "work");
  const shiplinoHome = daemonEnv(home).SHIPLINO_HOME!;
  mkdirSync(shiplinoHome, { recursive: true });
  // Two projects, detected from their marker files.
  mkdirSync(path.join(work, "demo-app", "src"), { recursive: true });
  writeFileSync(path.join(work, "demo-app", "package.json"), '{ "name": "demo-app" }\n');
  mkdirSync(path.join(work, "api-server"), { recursive: true });
  writeFileSync(path.join(work, "api-server", "go.mod"), "module example.com/api-server\n");

  const log = createWriteStream(logFile);
  const daemon = spawn(bin, ["daemon"], { env: daemonEnv(home), stdio: ["ignore", "pipe", "pipe"] });
  daemon.stdout.pipe(log);
  daemon.stderr.pipe(log);
  const exited = new Promise<number | null>((resolve) => daemon.once("exit", resolve));

  const teardown = async () => {
    if (daemon.exitCode === null) {
      daemon.kill("SIGTERM");
      await Promise.race([exited, new Promise((r) => setTimeout(r, 5000))]);
      if (daemon.exitCode === null) daemon.kill("SIGKILL");
    }
    rmSync(root, { recursive: true, force: true });
  };

  try {
    const url = await waitForDaemon(daemon, shiplinoHome);
    const token = readFileSync(path.join(shiplinoHome, "token"), "utf8").trim();
    seedSessions(home, work);
    await waitForSessions(url, token, {
      [seed.review.id]: "review",
      [seed.refuse.id]: "review",
      [seed.waiting.id]: "waiting",
      [seed.running.id]: "running",
      [seed.api.id]: "running",
    });
    process.env.SHIPLINO_E2E_URL = url;
    process.env.SHIPLINO_E2E_HOME = home;
    process.env.SHIPLINO_E2E_WORK = work;
  } catch (e) {
    await teardown();
    throw new Error(`${(e as Error).message}\n(daemon log: ${logFile})`);
  }
  return teardown;
}

/** waitForDaemon polls until the daemon has written its port and answers /health. */
async function waitForDaemon(daemon: ChildProcess, home: string): Promise<string> {
  const deadline = Date.now() + 30_000;
  while (Date.now() < deadline) {
    if (daemon.exitCode !== null) throw new Error(`the daemon exited with code ${daemon.exitCode}`);
    const portFile = path.join(home, "port");
    if (existsSync(portFile)) {
      const url = `http://127.0.0.1:${readFileSync(portFile, "utf8").trim()}`;
      const ok = await fetch(`${url}/api/v1/health`)
        .then((r) => r.ok)
        .catch(() => false);
      if (ok) {
        const page = await fetch(`${url}/`).then((r) => r.text());
        if (!page.includes("/_next/"))
          throw new Error(`${bin} was built without the web app: run "make build" with npm`);
        return url;
      }
    }
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error("the daemon didn't start within 30s");
}

/** waitForSessions polls the API until every seeded session has its expected status. */
async function waitForSessions(url: string, token: string, want: Record<string, string>) {
  const deadline = Date.now() + 30_000;
  let last = "";
  while (Date.now() < deadline) {
    const r = await fetch(`${url}/api/v1/sessions?limit=500`, { headers: { Authorization: `Bearer ${token}` } });
    const { sessions } = (await r.json()) as { sessions: { id: string; status: string }[] };
    const got = Object.fromEntries(sessions.map((s) => [s.id.replace(/^claude-code:/, ""), s.status]));
    last = JSON.stringify(got);
    if (Object.entries(want).every(([id, status]) => got[id] === status)) return;
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error(`seeded sessions didn't reach their states within 30s: ${last}`);
}

function seedSessions(home: string, work: string) {
  const demo = path.join(work, "demo-app");
  const send = (p: Record<string, unknown>) => hook(home, p);

  // Review: a finished turn with tool calls and an edit.
  {
    const ev = claude(seed.review.id, demo);
    const file = path.join(demo, "src", "greet.ts");
    send(ev("SessionStart", { source: "startup", model: "claude-sonnet-5-5" }));
    send(ev("UserPromptSubmit", { prompt_id: "p-1", prompt: seed.review.prompt }));
    const grep = { tool_name: "Grep", tool_input: { pattern: "greet", path: "src" }, tool_use_id: "tu-1" };
    send(ev("PreToolUse", grep));
    send(ev("PostToolUse", { ...grep, tool_response: { numFiles: 1 }, duration_ms: 40 }));
    const read = { tool_name: "Read", tool_input: { file_path: file }, tool_use_id: "tu-2" };
    send(ev("PreToolUse", read));
    send(ev("PostToolUse", { ...read, tool_response: {}, duration_ms: 5 }));
    const edit = {
      tool_name: "Edit",
      tool_input: { file_path: file, old_string: "export function greet", new_string: "export function sayHello" },
      tool_use_id: "tu-3",
    };
    send(ev("PreToolUse", edit));
    send(
      ev("PostToolUse", {
        ...edit,
        tool_response: {
          filePath: file,
          structuredPatch: [
            {
              oldStart: 1,
              oldLines: 3,
              newStart: 1,
              newLines: 3,
              lines: [
                "-export function greet(name: string) {",
                "+export function sayHello(name: string) {",
                '   return "Hello, " + name;',
                " }",
              ],
            },
          ],
        },
        duration_ms: 12,
      }),
    );
    const test = { tool_name: "Bash", tool_input: { command: "npm test -- greet" }, tool_use_id: "tu-4" };
    send(ev("PreToolUse", test));
    send(ev("PostToolUse", { ...test, tool_response: { stdout: "1 passing", stderr: "" }, duration_ms: 900 }));
    send(ev("Stop", { prompt_id: "p-1", last_assistant_message: "Renamed the helper." }));
  }

  // Review: a second finished card, used for the refused drop.
  {
    const ev = claude(seed.refuse.id, demo);
    send(ev("SessionStart", { source: "startup" }));
    send(ev("UserPromptSubmit", { prompt_id: "p-1", prompt: seed.refuse.prompt }));
    send(
      ev("PostToolUse", {
        tool_name: "Write",
        tool_input: { file_path: path.join(demo, "src", "log.ts"), content: "export const log = console;\n" },
        tool_response: { type: "create" },
        tool_use_id: "tu-1",
        duration_ms: 4,
      }),
    );
    send(ev("Stop", { prompt_id: "p-1", last_assistant_message: "Done." }));
  }

  // Waiting on a permission prompt: shows under "Needs you".
  {
    const ev = claude(seed.waiting.id, demo);
    send(ev("SessionStart", { source: "startup" }));
    send(ev("UserPromptSubmit", { prompt_id: "p-1", prompt: seed.waiting.prompt }));
    send(
      ev("Notification", {
        message: "Claude needs your permission to use Bash",
        notification_type: "permission_prompt",
      }),
    );
  }

  // Running, in each project.
  for (const s of [seed.running, seed.api]) {
    const ev = claude(s.id, path.join(work, s.project));
    send(ev("SessionStart", { source: "startup" }));
    send(ev("UserPromptSubmit", { prompt_id: "p-1", prompt: s.prompt }));
    send(ev("PreToolUse", { tool_name: "Bash", tool_input: { command: "ls" }, tool_use_id: "tu-1" }));
  }
}
