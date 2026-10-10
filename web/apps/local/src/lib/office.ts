// The virtual office: which sessions have a desk, what each character is
// doing, and where rooms and desks go. Pure functions, so they are unit
// tested without a browser; the canvas drawing lives in components/office.

import type { Session, WaitingReason } from "./api";

/** What a character is doing, from its session's status and "now doing". */
export type Pose =
  | "typing" // editing files or using a tool
  | "reading" // reading, searching or browsing
  | "terminal" // running a shell command
  | "thinking" // between tools
  | "waiting" // at the room's bell: the session needs you
  | "idle" // at the desk, nothing happening
  | "sleeping" // idle for a long time
  | "celebrating" // just finished
  | "left" // finished a while ago: the desk is empty
  | "failed"; // red alert

/** Finished sessions keep their desk this long after their last activity. */
export const KEEP_MS = 2 * 60 * 60 * 1000;
/** A finished session celebrates this long, then leaves its desk. */
export const CELEBRATE_MS = 2 * 60 * 1000;
/** An idle session falls asleep after this long without activity. */
export const SLEEP_MS = 15 * 60 * 1000;

const ms = (iso?: string) => (iso ? Date.parse(iso) : Number.NaN);

/** Time since the session last did something (its end, if it ended). */
function quietFor(s: Session, now: number): number {
  const at = ms(s.ended_at);
  return now - (Number.isNaN(at) ? ms(s.last_event_at) : at);
}

// "now doing" is written by the engine as "<Verb> <target>" (see nowDoing in
// pkg/engine): the verb tells the tool kind without needing the tool name.
const verbs: [string, Pose][] = [
  ["Editing ", "typing"],
  ["Reading ", "reading"],
  ["Searching ", "reading"],
  ["Browsing ", "reading"],
  ["Running ", "terminal"],
  ["Calling ", "typing"],
  ["Delegating", "typing"],
  ["Using ", "typing"],
];

/** pose maps a session to what its character does at time now. */
export function pose(s: Session, now: number): Pose {
  switch (s.status) {
    case "waiting":
      return "waiting";
    case "failed":
      return "failed";
    case "done":
    case "review":
      return quietFor(s, now) < CELEBRATE_MS ? "celebrating" : "left";
    case "idle":
      return quietFor(s, now) >= SLEEP_MS ? "sleeping" : "idle";
  }
  const doing = s.now_doing ?? "";
  for (const [prefix, p] of verbs) if (doing.startsWith(prefix)) return p;
  return "thinking";
}

/**
 * present says whether a session has a desk now: live sessions always,
 * finished and failed ones for a while (subagents only while they
 * celebrate, there are many and they are short).
 */
export function present(s: Session, now: number): boolean {
  if (s.status === "running" || s.status === "waiting") return true;
  const quiet = quietFor(s, now);
  if (s.parent_id) return quiet < CELEBRATE_MS;
  return quiet < KEEP_MS;
}

/** Short, human description of a pose, for labels and the hover card. */
export function poseLabel(p: Pose, reason?: WaitingReason): string {
  switch (p) {
    case "typing":
      return "Typing";
    case "reading":
      return "Reading";
    case "terminal":
      return "At the terminal";
    case "thinking":
      return "Thinking";
    case "waiting":
      return reason === "permission"
        ? "At the bell: needs approval"
        : reason === "question"
          ? "At the bell: has a question"
          : reason === "idle"
            ? "At the bell: your turn"
            : "At the bell: waiting on you";
    case "idle":
      return "Idle";
    case "sleeping":
      return "Asleep (no activity for a while)";
    case "celebrating":
      return "Celebrating: done";
    case "left":
      return "Done, left the desk";
    case "failed":
      return "Red alert: failed";
  }
}

/** hash is a small, stable string hash (FNV-1a), used to vary looks. */
export function hash(s: string): number {
  let h = 0x811c9dc5;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return h >>> 0;
}

export interface Room {
  id: string;
  /** Session ids, in desk order: top-level sessions by start, each followed by its subagents. */
  members: string[];
}

/** rooms groups the present sessions by project, in a stable order. */
export function rooms(sessions: Session[], now: number): Room[] {
  const here = sessions.filter((s) => present(s, now));
  const ids = new Set(here.map((s) => s.id));
  const byStart = (a: Session, b: Session) => a.started_at.localeCompare(b.started_at) || a.id.localeCompare(b.id);
  const kids = new Map<string, Session[]>();
  for (const s of here) {
    if (s.parent_id && ids.has(s.parent_id)) kids.set(s.parent_id, [...(kids.get(s.parent_id) ?? []), s]);
  }
  const groups = new Map<string, string[]>();
  const place = (s: Session) => {
    const key = s.project_id || "unsorted";
    const list = groups.get(key) ?? [];
    list.push(s.id);
    groups.set(key, list);
    for (const k of (kids.get(s.id) ?? []).sort(byStart)) place(k);
  };
  // Roots, and subagents whose parent has no desk, sit in start order.
  for (const s of here.filter((s) => !s.parent_id || !ids.has(s.parent_id)).sort(byStart)) place(s);
  return [...groups].map(([id, members]) => ({ id, members })).sort((a, b) => a.id.localeCompare(b.id));
}

// Sizes in art pixels (the canvas is drawn at 1x and scaled up).
export const DESK_W = 40;
export const DESK_H = 40;
export const ROOM_PAD = 8;
/** Room top band, under its name label. */
export const ROOM_HEAD = 16;
/** Room bottom band: the door, the bell and the queue in front of it. */
export const ROOM_FOOT = 28;
export const GAP = 8;
const MAX_COLS = 4;

export interface Point {
  x: number;
  y: number;
}

export interface RoomBox extends Point {
  id: string;
  w: number;
  h: number;
  bell: Point;
  door: Point;
}

export interface Desk extends Point {
  id: string;
  room: string;
  /** Where the character stands while it waits on you: in the queue at the bell. */
  stand?: Point;
}

export interface Layout {
  width: number;
  height: number;
  rooms: RoomBox[];
  desks: Desk[];
}

/**
 * layout places rooms left to right, wrapping at maxWidth, with desks in a
 * grid inside each room. Waiting characters queue to the left of the bell,
 * in desk order (wrapping to a second line when the queue is long).
 */
export function layout(list: Room[], maxWidth: number, waiting: (id: string) => boolean = () => false): Layout {
  const fit = Math.max(1, Math.floor((maxWidth - 2 * ROOM_PAD) / DESK_W));
  const out: Layout = { width: 0, height: 0, rooms: [], desks: [] };
  let x = 0;
  let y = 0;
  let rowH = 0;
  for (const r of list) {
    const cols = Math.max(2, Math.min(MAX_COLS, fit, r.members.length));
    const rows = Math.ceil(r.members.length / cols);
    const w = 2 * ROOM_PAD + cols * DESK_W;
    const h = ROOM_HEAD + rows * DESK_H + ROOM_FOOT;
    if (x > 0 && x + w > maxWidth) {
      x = 0;
      y += rowH + GAP;
      rowH = 0;
    }
    const foot = y + h - ROOM_FOOT / 2;
    const box: RoomBox = {
      id: r.id,
      x,
      y,
      w,
      h,
      bell: { x: x + w - ROOM_PAD - 6, y: foot },
      door: { x: x + ROOM_PAD, y: y + h },
    };
    out.rooms.push(box);
    const queue = Math.max(1, Math.floor((w - 2 * ROOM_PAD - 16) / 12));
    let n = 0;
    r.members.forEach((id, i) => {
      const desk: Desk = {
        id,
        room: r.id,
        x: x + ROOM_PAD + (i % cols) * DESK_W,
        y: y + ROOM_HEAD + Math.floor(i / cols) * DESK_H,
      };
      if (waiting(id)) {
        const line = Math.floor(n / queue) % 2;
        desk.stand = { x: box.bell.x - 12 - (n % queue) * 12, y: y + h - 5 - line * 6 };
        n++;
      }
      out.desks.push(desk);
    });
    x += w + GAP;
    rowH = Math.max(rowH, h);
    out.width = Math.max(out.width, x - GAP);
    out.height = Math.max(out.height, y + h);
  }
  return out;
}
