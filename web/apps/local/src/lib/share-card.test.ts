import assert from "node:assert/strict";
import { test } from "node:test";
import type { InsightRow, Insights, InsightTotals } from "./api";
import { buildWeekCard } from "./share-card.ts";

const totals = (over: Partial<InsightTotals> = {}): InsightTotals => ({
  sessions: 0,
  cost_usd: 0,
  active_ms: 0,
  waiting_ms: 0,
  turns: 0,
  tool_calls: 0,
  tool_errors: 0,
  files: 0,
  lines_added: 0,
  lines_removed: 0,
  commits: 0,
  prs: 0,
  prs_merged: 0,
  input_tokens: 0,
  output_tokens: 0,
  cache_read_tokens: 0,
  cache_write_tokens: 0,
  ...over,
});

const row = (key: string, sessions: number, active_ms: number, name?: string): InsightRow => ({
  key,
  name,
  sessions,
  cost_usd: 1,
  active_ms,
  input_tokens: 0,
  output_tokens: 0,
  cache_read_tokens: 0,
  lines_added: 0,
  lines_removed: 0,
});

const day = (date: string, sessions: number) => ({ date, sessions, cost_usd: {}, active_ms: 0 });

// Synthetic week: project names, ids and paths that must not leak.
const week: Insights = {
  from: "2026-10-04T00:00:00Z",
  to: "2026-10-11T00:00:00Z",
  days: 7,
  totals: totals({
    sessions: 9,
    cost_usd: 12.5,
    active_ms: 5 * 3_600_000,
    prs: 3,
    prs_merged: 2,
    input_tokens: 1000,
    output_tokens: 200,
    cache_read_tokens: 5000,
    cache_write_tokens: 300,
  }),
  previous: totals(),
  daily: [
    day("2026-10-04", 0),
    day("2026-10-05", 1),
    day("2026-10-06", 4),
    day("2026-10-07", 0),
    day("2026-10-08", 1),
    day("2026-10-09", 2),
    day("2026-10-10", 1),
  ],
  agents: [
    row("codex", 2, 3_600_000),
    row("claude-code", 6, 4 * 3_600_000),
    row("cursor", 1, 60_000),
    row("gemini-cli", 0, 0),
  ],
  projects: [row("/home/dev/secret-client", 6, 1, "secret-client"), row("p-internal-id", 3, 1, "acme-billing")],
  models: [row("model-x", 9, 1)],
  tools: [{ tool: "Bash", kind: "shell", calls: 3 }],
  cost_sources: {},
  failures: {
    tool_failures: 0,
    shell_failures: 0,
    denials: 0,
    retry_loops: 0,
    ended_badly: 0,
    tools: [],
    shell: [],
    loops: [],
    endings: [],
    agents: [],
    projects: [],
  },
  authorship: {
    totals: { key: "", commits: 0, agent_lines: 0, human_lines: 0, unknown_lines: 0 },
    daily: [],
    agents: [],
    projects: [],
  },
};

const secrets = ["secret-client", "acme-billing", "p-internal-id", "/home/dev", "model-x", "Bash"];

test("redacted by default: only aggregates and agent names", () => {
  const card = buildWeekCard(week, undefined, { includeProjects: false, apiEquivalent: true });
  const json = JSON.stringify(card);
  for (const s of secrets) assert.ok(!json.includes(s), `card leaks ${s}: ${json}`);
  assert.equal(card.projectNames, undefined);
  assert.equal(card.projectCount, 2);
  assert.equal(card.sessions, 9);
  assert.equal(card.tokens, 6500);
  assert.equal(card.prsMerged, 2);
  assert.equal(card.apiEquivalent, true);
  assert.deepEqual(
    card.topAgents.map((a) => a.name),
    ["Claude Code", "Codex", "Cursor"],
  );
  assert.deepEqual(card.busiestDay, { date: "2026-10-06", sessions: 4 });
  assert.equal(card.activeDays, 5);
  assert.equal(card.streak, 3);
  assert.equal(card.from, "2026-10-04");
  assert.equal(card.to, "2026-10-10");
});

test("project names only when opted in, never ids or paths", () => {
  const card = buildWeekCard(week, undefined, { includeProjects: true, apiEquivalent: false });
  assert.deepEqual(card.projectNames, ["secret-client", "acme-billing"]);
  const json = JSON.stringify(card);
  for (const s of ["p-internal-id", "/home/dev", "model-x"]) assert.ok(!json.includes(s), `card leaks ${s}`);
});

test("streak uses the longer history and skips a quiet today", () => {
  const history = [day("2026-10-07", 1), day("2026-10-08", 1), day("2026-10-09", 1), day("2026-10-10", 0)];
  const card = buildWeekCard(week, history, { includeProjects: false, apiEquivalent: false });
  assert.equal(card.streak, 3);
  assert.equal(card.streakAtLeast, true);
  const empty = buildWeekCard({ ...week, daily: [] }, [], { includeProjects: false, apiEquivalent: false });
  assert.equal(empty.streak, 0);
  assert.equal(empty.busiestDay, undefined);
});
