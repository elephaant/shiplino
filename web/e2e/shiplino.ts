// Helpers shared by the global setup and the tests: where the binary is,
// the environment of the test daemon, and the real hook shim.

import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import path from "node:path";

export const repoRoot = path.resolve(__dirname, "..", "..");

/** bin is the shiplino binary under test: $SHIPLINO_BIN, else make build's output. */
export const bin =
  process.env.SHIPLINO_BIN || path.join(repoRoot, "bin", process.platform === "win32" ? "shiplino.exe" : "shiplino");

/** Values the global setup publishes to the test workers (they inherit process.env). */
export const env = {
  get url() {
    return must("SHIPLINO_E2E_URL");
  },
  get home() {
    return must("SHIPLINO_E2E_HOME");
  },
  get work() {
    return must("SHIPLINO_E2E_WORK");
  },
};

function must(key: string): string {
  const v = process.env[key];
  if (!v) throw new Error(`${key} is not set: run the tests with "npx playwright test" so the global setup runs`);
  return v;
}

/** daemonEnv is the environment of the test daemon and its hooks: a temp HOME, never the real one. */
export function daemonEnv(home: string): NodeJS.ProcessEnv {
  return {
    PATH: process.env.PATH,
    HOME: home,
    USERPROFILE: home,
    SHIPLINO_HOME: path.join(home, ".shiplino"),
  };
}

/**
 * hook runs the real hook shim with a Claude Code payload, exactly like the
 * agent would. It also checks the zero-token contract: no output, exit 0.
 */
export function hook(home: string, payload: Record<string, unknown>) {
  if (!existsSync(bin)) throw new Error(`${bin} not found: run "make build" first`);
  const r = spawnSync(bin, ["hook", "--agent", "claude-code"], {
    input: JSON.stringify(payload),
    env: daemonEnv(home),
    encoding: "utf8",
  });
  if (r.status !== 0 || r.stdout !== "" || r.stderr !== "") {
    throw new Error(`hook broke the zero-token contract: exit=${r.status} stdout=${r.stdout} stderr=${r.stderr}`);
  }
}

/** claude builds Claude Code hook payloads for one session in one folder. */
export function claude(session: string, cwd: string) {
  return (event: string, extra: Record<string, unknown> = {}) => ({
    session_id: session,
    cwd,
    hook_event_name: event,
    ...extra,
  });
}

/** Seeded sessions. Titles come from the first prompt. */
export const seed = {
  review: { id: "e2e-review", project: "demo-app", prompt: "Rename the greeting helper" },
  refuse: { id: "e2e-refuse", project: "demo-app", prompt: "Tidy up the logging setup" },
  waiting: { id: "e2e-waiting", project: "demo-app", prompt: "Add input validation to the signup form" },
  running: { id: "e2e-running", project: "demo-app", prompt: "Update the changelog for the release" },
  api: { id: "e2e-api", project: "api-server", prompt: "Add a health check endpoint" },
} as const;
