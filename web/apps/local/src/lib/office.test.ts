import assert from "node:assert/strict";
import { test } from "node:test";
import type { Session } from "./api";
import {
  CELEBRATE_MS,
  DESK_H,
  DESK_W,
  KEEP_MS,
  layout,
  type Pose,
  pose,
  poseLabel,
  present,
  ROOM_FOOT,
  rooms,
  SLEEP_MS,
} from "./office.ts";

const NOW = Date.parse("2026-10-10T12:00:00Z");
const ago = (ms: number) => new Date(NOW - ms).toISOString();

const session = (over: Partial<Session> = {}): Session => ({
  id: "s1",
  agent: "claude-code",
  root_id: "s1",
  project_id: "example.com/acme/app",
  status: "running",
  started_at: ago(60_000),
  last_event_at: ago(1000),
  turns: 1,
  tool_calls: 0,
  tool_errors: 0,
  lines_added: 0,
  lines_removed: 0,
  input_tokens: 0,
  output_tokens: 0,
  cache_read_tokens: 0,
  cache_write_tokens: 0,
  cost_usd: 0,
  tree_cost_usd: 0,
  best_cost_usd: 0,
  waiting_ms: 0,
  ...over,
});

test("pose follows the session's status and what it is doing", () => {
  const cases: [string, Partial<Session>, Pose][] = [
    ["edit", { now_doing: "Editing main.go" }, "typing"],
    ["write", { now_doing: "Editing notes.md" }, "typing"],
    ["read", { now_doing: "Reading README.md" }, "reading"],
    ["search", { now_doing: "Searching TODO" }, "reading"],
    ["web", { now_doing: "Browsing example.com" }, "reading"],
    ["shell", { now_doing: "Running go test ./..." }, "terminal"],
    ["mcp", { now_doing: "Calling issues.list" }, "typing"],
    ["task", { now_doing: "Delegating: review the diff" }, "typing"],
    ["other tool", { now_doing: "Using Frobnicate" }, "typing"],
    ["thinking", { now_doing: "Thinking…" }, "thinking"],
    ["nothing yet", {}, "thinking"],
    ["a verb inside a word is not a match", { now_doing: "Reader mode" }, "thinking"],
    ["waiting", { status: "waiting", waiting_reason: "permission", now_doing: "Running ls" }, "waiting"],
    ["failed", { status: "failed", now_doing: "API Error" }, "failed"],
    ["idle", { status: "idle", last_event_at: ago(SLEEP_MS - 1) }, "idle"],
    ["asleep", { status: "idle", last_event_at: ago(SLEEP_MS) }, "sleeping"],
    ["just done", { status: "done", ended_at: ago(CELEBRATE_MS - 1) }, "celebrating"],
    ["done a while ago", { status: "done", ended_at: ago(CELEBRATE_MS) }, "left"],
    ["review counts as done", { status: "review", last_event_at: ago(1000) }, "celebrating"],
    [
      "the end time wins over last activity",
      { status: "done", ended_at: ago(CELEBRATE_MS), last_event_at: ago(1) },
      "left",
    ],
  ];
  for (const [name, over, want] of cases) assert.equal(pose(session(over), NOW), want, name);
});

test("who has a desk", () => {
  const cases: [string, Partial<Session>, boolean][] = [
    ["running, however old", { status: "running", last_event_at: ago(KEEP_MS * 10) }, true],
    ["waiting, however old", { status: "waiting", last_event_at: ago(KEEP_MS * 10) }, true],
    ["recently done", { status: "done", ended_at: ago(KEEP_MS - 1) }, true],
    ["done long ago", { status: "done", ended_at: ago(KEEP_MS) }, false],
    ["recently failed", { status: "failed", last_event_at: ago(KEEP_MS - 1) }, true],
    ["idle long ago", { status: "idle", last_event_at: ago(KEEP_MS + 1) }, false],
    ["subagent while it celebrates", { parent_id: "p", status: "done", ended_at: ago(CELEBRATE_MS - 1) }, true],
    ["subagent after", { parent_id: "p", status: "done", ended_at: ago(CELEBRATE_MS) }, false],
    ["running subagent", { parent_id: "p", status: "running" }, true],
  ];
  for (const [name, over, want] of cases) assert.equal(present(session(over), NOW), want, name);
});

test("every waiting reason has its own label", () => {
  const labels = new Set((["permission", "question", "idle", undefined] as const).map((r) => poseLabel("waiting", r)));
  assert.equal(labels.size, 4);
  assert.match(poseLabel("waiting", "permission"), /approval/);
});

test("rooms group by project, subagents sit next to their parent", () => {
  const list = [
    session({ id: "b", project_id: "p2", started_at: ago(5000) }),
    session({ id: "a", project_id: "p1", started_at: ago(9000) }),
    session({ id: "a-sub", project_id: "p1", parent_id: "a", started_at: ago(1000) }),
    session({ id: "c", project_id: "p1", started_at: ago(3000) }),
    session({ id: "orphan", project_id: "p1", parent_id: "gone", started_at: ago(8000) }),
    session({ id: "old", project_id: "p1", status: "done", ended_at: ago(KEEP_MS * 2) }),
    session({ id: "none", project_id: undefined }),
  ];
  assert.deepEqual(rooms(list, NOW), [
    { id: "p1", members: ["a", "a-sub", "orphan", "c"] },
    { id: "p2", members: ["b"] },
    { id: "unsorted", members: ["none"] },
  ]);
  // Same input in another order: same office.
  assert.deepEqual(rooms([...list].reverse(), NOW), rooms(list, NOW));
});

const overlaps = (a: { x: number; y: number; w: number; h: number }, b: typeof a) =>
  a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h;

test("layout: rooms fit the width and never overlap, desks stay inside their room", () => {
  const list = Array.from({ length: 7 }, (_, r) => ({
    id: `p${r}`,
    members: Array.from({ length: (r * 3) % 11 || 1 }, (_, i) => `p${r}-s${i}`),
  }));
  for (const width of [200, 400, 640, 1000]) {
    const plan = layout(list, width, (id) => id.endsWith("s1") || id.endsWith("s2"));
    assert.equal(
      plan.desks.length,
      list.reduce((n, r) => n + r.members.length, 0),
    );
    assert.ok(plan.width <= width, `width ${plan.width} > ${width}`);
    plan.rooms.forEach((a, i) => {
      for (const b of plan.rooms.slice(i + 1)) assert.ok(!overlaps(a, b), `${a.id} overlaps ${b.id}`);
    });
    for (const d of plan.desks) {
      const room = plan.rooms.find((r) => r.id === d.room)!;
      assert.ok(d.x >= room.x && d.x + DESK_W <= room.x + room.w, `${d.id} outside ${room.id} (x)`);
      assert.ok(d.y >= room.y && d.y + DESK_H <= room.y + room.h - ROOM_FOOT, `${d.id} outside ${room.id} (y)`);
      for (const e of plan.desks) {
        if (e !== d) assert.ok(!overlaps({ ...d, w: DESK_W, h: DESK_H }, { ...e, w: DESK_W, h: DESK_H }));
      }
    }
  }
});

test("layout: waiting characters queue at the bell, others don't get a spot", () => {
  const plan = layout([{ id: "p", members: ["a", "b", "c", "d"] }], 400, (id) => id === "b" || id === "d");
  const [room] = plan.rooms;
  const spot = (id: string) => plan.desks.find((d) => d.id === id)!.stand;
  assert.equal(spot("a"), undefined);
  assert.equal(spot("c"), undefined);
  const b = spot("b")!;
  const d = spot("d")!;
  assert.ok(b.x < room!.bell.x && b.x > d.x, "first in line stands next to the bell");
  assert.ok(b.y > room!.y + room!.h - ROOM_FOOT && b.y < room!.y + room!.h, "in the room's bottom band");
});

test("layout: 60 characters", () => {
  const list = Array.from({ length: 6 }, (_, r) => ({
    id: `p${r}`,
    members: Array.from({ length: 10 }, (_, i) => `${r}-${i}`),
  }));
  const plan = layout(list, 560, () => true);
  assert.equal(plan.desks.length, 60);
  assert.ok(plan.desks.every((d) => d.stand));
  assert.ok(plan.width <= 560);
});
