// Draws the office on a plain 2D canvas at 1 pixel per art pixel; CSS
// scales it up with `image-rendering: pixelated`. Animation runs at most
// FPS frames a second, stops while the tab is hidden, and is off for
// reduced motion (then a frame is drawn only when the scene changes).

import type { WaitingReason } from "@/lib/api";
import { type Desk, hash, type Layout, type Point, type Pose } from "@/lib/office";
import {
  bell,
  type Colors,
  cap,
  chair,
  cheer1,
  cheer2,
  draw,
  glyph,
  hairs,
  monitor,
  plant,
  seated,
  skins,
  standing,
  trousers,
  walkL,
  walkR,
} from "./sprites";

const FPS = 15;
/** Sprite frames advance at this rate (typing, blinking, walking steps). */
const TICK_MS = 160;
/** Walking speed in art pixels a second. */
const SPEED = 48;

export interface Actor {
  id: string;
  desk: Desk;
  pose: Pose;
  /** The agent's color as a CSS variable, var(--agent-…): the character's shirt. */
  shirt: string;
  sub: boolean;
  reason?: WaitingReason;
}

export interface Scene {
  layout: Layout;
  actors: Actor[];
}

interface Theme {
  floor: string;
  floorAlt: string;
  wall: string;
  primary: string;
  bubble: string;
  bubbleText: string;
  running: string;
  waiting: string;
  review: string;
  done: string;
  failed: string;
}

// Furniture colors are part of the art, chosen to read on both themes.
const furniture: Colors = {
  w: "#2c323d",
  x: "#141820",
  t: "#545c69",
  b: "#5b6474",
  o: "#3d4451",
  g: "#4f9a5c",
  G: "#3a7a47",
  r: "#b4693f",
};
const DESK = "#b98a5d";
const DESK_EDGE = "#8e6441";
const KEYS = "#d6dae1";
const SCREEN = "#1b2130";
const PAGE = "#e9edf3";
const INK = "#8f99a8";

/** seatPoint is where a character's feet are when it stands up from its desk. */
const seatPoint = (d: Desk): Point => ({ x: d.x + 20, y: d.y + 38 });

interface Walker {
  pos: Point | null; // null: seated (or standing in place) at the desk
}

export class OfficeRenderer {
  private ctx: CanvasRenderingContext2D;
  private scene: Scene = { layout: { width: 0, height: 0, rooms: [], desks: [] }, actors: [] };
  private theme: Theme | null = null;
  private background: HTMLCanvasElement | null = null;
  private walkers = new Map<string, Walker>();
  private frame = 0;
  private timer: ReturnType<typeof setTimeout> | undefined;
  private last = 0;
  private highlight: string | null = null;
  private reduced = false;
  private colors = new Map<string, string>();

  constructor(private canvas: HTMLCanvasElement) {
    this.ctx = canvas.getContext("2d")!;
    document.addEventListener("visibilitychange", this.onVisibility);
  }

  destroy() {
    this.stop();
    document.removeEventListener("visibilitychange", this.onVisibility);
  }

  /** setReduced switches between animated and static mode. */
  setReduced(reduced: boolean) {
    this.reduced = reduced;
    if (reduced) {
      this.stop();
      for (const w of this.walkers.values()) w.pos = null;
    }
    this.kick();
  }

  /** refreshTheme reads the theme tokens again (after a light/dark switch). */
  refreshTheme() {
    const css = getComputedStyle(this.canvas);
    const v = (name: string) => css.getPropertyValue(name).trim();
    this.theme = {
      floor: v("--card"),
      floorAlt: v("--muted"),
      wall: v("--border"),
      primary: v("--primary"),
      bubble: v("--popover"),
      bubbleText: v("--popover-foreground"),
      running: v("--status-running"),
      waiting: v("--status-waiting"),
      review: v("--status-review"),
      done: v("--status-done"),
      failed: v("--status-failed"),
    };
    this.colors.clear();
    this.background = null;
    this.kick();
  }

  /** resolve turns var(--x) into the color it holds now (the canvas can't read variables). */
  private resolve(value: string): string {
    let c = this.colors.get(value);
    if (c === undefined) {
      const name = /^var\((--[\w-]+)\)$/.exec(value)?.[1];
      c = name ? getComputedStyle(this.canvas).getPropertyValue(name).trim() : value;
      this.colors.set(value, c);
    }
    return c;
  }

  setScene(scene: Scene) {
    if (scene.layout !== this.scene.layout) {
      this.background = null;
      // A new layout moves desks: characters on their way somewhere jump to it.
      const before = new Map(this.scene.actors.map((a) => [a.id, a.desk]));
      for (const a of scene.actors) {
        const d = before.get(a.id);
        const w = this.walkers.get(a.id);
        if (w?.pos && d && (d.x !== a.desk.x || d.y !== a.desk.y || !same(d.stand, a.desk.stand))) {
          w.pos = a.pose === "waiting" && a.desk.stand ? { ...a.desk.stand } : null;
        }
      }
    }
    this.scene = scene;
    const ids = new Set(scene.actors.map((a) => a.id));
    for (const id of this.walkers.keys()) if (!ids.has(id)) this.walkers.delete(id);
    for (const a of scene.actors) if (!this.walkers.has(a.id)) this.walkers.set(a.id, { pos: null });
    if (this.canvas.width !== scene.layout.width || this.canvas.height !== scene.layout.height) {
      this.canvas.width = scene.layout.width;
      this.canvas.height = scene.layout.height;
    }
    this.kick();
  }

  setHighlight(id: string | null) {
    this.highlight = id;
    this.kick();
  }

  private onVisibility = () => this.kick();

  /** kick draws now, and keeps animating unless motion is reduced or the tab is hidden. */
  private kick() {
    if (!this.theme) return;
    if (document.hidden) return this.stop();
    if (this.reduced) {
      this.stop();
      this.draw(0, 0);
      return;
    }
    if (!this.frame && !this.timer) this.frame = requestAnimationFrame(this.loop);
  }

  private stop() {
    cancelAnimationFrame(this.frame);
    clearTimeout(this.timer);
    this.frame = 0;
    this.timer = undefined;
    this.last = 0;
  }

  // Each frame is drawn in an animation frame (in step with the screen), and
  // the next one is asked for only after a timer, so the page wakes up FPS
  // times a second rather than at the display's refresh rate.
  private loop = (t: number) => {
    this.frame = 0;
    if (document.hidden || this.reduced) return;
    const dt = this.last ? Math.min(0.2, (t - this.last) / 1000) : 0;
    this.last = t;
    this.draw(t, dt);
    this.timer = setTimeout(
      () => {
        this.timer = undefined;
        if (!document.hidden && !this.reduced) this.frame = requestAnimationFrame(this.loop);
      },
      1000 / FPS - 4,
    );
  };

  private draw(t: number, dt: number) {
    const th = this.theme;
    if (!th) return;
    const { ctx } = this;
    const { layout, actors } = this.scene;
    ctx.clearRect(0, 0, layout.width, layout.height);
    if (!this.background) this.background = this.paintBackground(th);
    ctx.drawImage(this.background, 0, 0);
    const tick = Math.floor(t / TICK_MS);

    const ringing = new Set(actors.filter((a) => a.pose === "waiting").map((a) => a.desk.room));
    for (const r of layout.rooms) {
      const wiggle = ringing.has(r.id) && !this.reduced ? (tick % 2 ? -1 : 1) : 0;
      draw(ctx, bell, { y: th.waiting, Y: "#fff6d5", t: furniture.t! }, r.bell.x - 5 + wiggle, r.bell.y - 6);
    }

    const away: { a: Actor; at: Point; moving: boolean }[] = [];
    for (const a of actors) {
      const w = this.walkers.get(a.id)!;
      const at = this.move(a, w, dt);
      this.paintDesk(a, at === null, th, tick + (hash(a.id) % 7));
      if (at) away.push({ a, at, moving: !!w.pos && !same(at, a.desk.stand) });
    }
    away.sort((p, q) => p.at.y - q.at.y);
    for (const { a, at, moving } of away) {
      const look = this.look(a);
      const step = moving ? (tick % 2 ? walkL : walkR) : standing;
      draw(ctx, step, look, at.x - 6, at.y - 20);
      if (a.sub) draw(ctx, cap, look, at.x - 6, at.y - 20);
      if (!moving && a.pose === "waiting") this.bubble(at.x - 5, at.y - 31, a.reason, th);
      if (this.highlight === a.id) ring(ctx, at.x - 8, at.y - 22, 16, 24, th.primary);
    }
  }

  /**
   * move advances a walking character and returns where it stands, or null
   * when it sits at its desk. Waiting characters walk down to the corridor,
   * then along it to the bell; they walk back the other way.
   */
  private move(a: Actor, w: Walker, dt: number): Point | null {
    const seat = seatPoint(a.desk);
    const goal = a.pose === "waiting" ? (a.desk.stand ?? seat) : null;
    if (this.reduced || dt === 0) {
      if (this.reduced) w.pos = goal ? { ...goal } : null;
      else if (goal && !w.pos) w.pos = { ...goal }; // first frame: already there
      return w.pos;
    }
    if (goal) {
      w.pos ??= { ...seat };
      step(w.pos, goal, SPEED * dt, "y");
      return w.pos;
    }
    if (!w.pos) return null;
    step(w.pos, seat, SPEED * dt, "x");
    if (same(w.pos, seat)) w.pos = null;
    return w.pos;
  }

  private look(a: Actor): Colors {
    const h = hash(a.id);
    return {
      h: hairs[h % hairs.length]!,
      s: skins[(h >>> 4) % skins.length]!,
      e: "#20242c",
      c: this.resolve(a.shirt),
      d: "#2a2f3a",
      p: trousers[(h >>> 8) % trousers.length]!,
      k: "#1d2027",
    };
  }

  private paintBackground(th: Theme): HTMLCanvasElement {
    const { layout } = this.scene;
    const c = document.createElement("canvas");
    c.width = Math.max(1, layout.width);
    c.height = Math.max(1, layout.height);
    const ctx = c.getContext("2d")!;
    for (const r of layout.rooms) {
      ctx.fillStyle = th.floor;
      ctx.fillRect(r.x, r.y, r.w, r.h);
      ctx.fillStyle = th.floorAlt;
      for (let y = r.y; y < r.y + r.h; y += 8) {
        for (let x = r.x + (((y - r.y) / 8) % 2) * 8; x < r.x + r.w; x += 16) {
          ctx.fillRect(x, y, Math.min(8, r.x + r.w - x), Math.min(8, r.y + r.h - y));
        }
      }
      ctx.fillStyle = th.wall;
      ctx.fillRect(r.x, r.y, r.w, 3); // back wall
      ctx.fillRect(r.x, r.y, 2, r.h);
      ctx.fillRect(r.x + r.w - 2, r.y, 2, r.h);
      ctx.fillRect(r.x, r.y + r.h - 2, r.w, 2);
      ctx.clearRect(r.door.x, r.door.y - 2, 16, 2); // the door
      ctx.fillStyle = th.floorAlt;
      ctx.fillRect(r.door.x + 2, r.door.y - 6, 12, 4); // doormat
      draw(ctx, plant, furniture, r.x + r.w - 14, r.y + 5);
    }
    return c;
  }

  /** paintDesk draws one desk, its screen, and the character when it is at the desk. */
  private paintDesk(a: Actor, home: boolean, th: Theme, tick: number) {
    const { ctx } = this;
    const { x, y } = a.desk;
    const look = this.look(a);
    draw(ctx, monitor, furniture, x + 11, y);
    this.screen(a.pose, home, x + 12, y + 1, th, tick, this.resolve(a.shirt));
    ctx.fillStyle = DESK;
    ctx.fillRect(x + 4, y + 12, 32, 9);
    ctx.fillStyle = DESK_EDGE;
    ctx.fillRect(x + 4, y + 21, 32, 3);
    ctx.fillStyle = KEYS;
    ctx.fillRect(x + 14, y + 16, 12, 2);
    draw(ctx, monitor.slice(11), furniture, x + 11, y + 11); // the stand sits on the desk

    switch (a.pose) {
      case "left":
        ctx.fillStyle = th.done;
        ctx.fillRect(x + 6, y + 13, 7, 6);
        glyph(ctx, "check", "#ffffff", x + 7, y + 14);
        break;
      case "failed": {
        const on = this.reduced || tick % 4 < 2;
        if (on) {
          ctx.globalAlpha = 0.35;
          ctx.fillStyle = th.failed;
          ctx.fillRect(x + 29, y + 6, 7, 7);
          ctx.globalAlpha = 1;
        }
        ctx.fillStyle = on ? th.failed : DESK_EDGE;
        ctx.fillRect(x + 31, y + 8, 3, 3);
        break;
      }
    }

    if (home && a.pose !== "left" && a.pose !== "waiting") {
      if (a.pose === "celebrating") {
        const up = this.reduced || tick % 2 === 0;
        draw(ctx, up ? cheer1 : cheer2, look, x + 14, y + 18);
        this.confetti(x, y, tick, th);
      } else {
        const sleeping = a.pose === "sleeping";
        const head = sleeping ? -2 : 0;
        draw(ctx, seated.slice(0, 6), look, x + 14, y + 20 + head);
        if (a.sub) draw(ctx, cap, look, x + 14, y + 20 + head);
        draw(ctx, seated.slice(6), look, x + 14, y + 26);
        if (a.pose === "typing" || a.pose === "terminal") {
          ctx.fillStyle = look.s!;
          ctx.fillRect(x + 15, y + 18 + (tick % 2), 2, 1);
          ctx.fillRect(x + 23, y + 18 + ((tick + 1) % 2), 2, 1);
        }
        if (sleeping) {
          const rise = this.reduced ? 0 : tick % 6;
          glyph(ctx, "z", th.review, x + 29, y + 14 - rise);
        }
        if (a.pose === "thinking") this.bubble(x + 24, y + 9, "thinking", th);
        draw(ctx, chair, furniture, x + 12, y + 31);
      }
    } else if (a.pose !== "celebrating") {
      draw(ctx, chair, furniture, x + 12, y + 31);
    }
    if (this.highlight === a.id && home) ring(ctx, x + 1, y, 38, 38, th.primary);
  }

  /** screen draws what's on a monitor: a 16x9 area at (x, y). */
  private screen(p: Pose, home: boolean, x: number, y: number, th: Theme, tick: number, accent: string) {
    const { ctx } = this;
    const t = this.reduced ? 3 : tick;
    const fill = (c: string) => {
      ctx.fillStyle = c;
      ctx.fillRect(x, y, 16, 9);
    };
    const line = (c: string, lx: number, ly: number, w: number) => {
      ctx.fillStyle = c;
      ctx.fillRect(x + lx, y + ly, Math.max(0, Math.min(w, 16 - lx - 1)), 1);
    };
    if (!home && p !== "waiting") return fill(furniture.x!);
    switch (p) {
      case "typing":
        fill(SCREEN);
        line(accent, 1, 1, 9);
        line(INK, 3, 3, 7);
        line(accent, 3, 5, 4);
        line(INK, 1, 7, (t % 12) + 1);
        return;
      case "reading": {
        fill(PAGE);
        for (let i = 0; i < 4; i++) line(INK, 2, ((i * 2 + 1 - (t % 2) + 8) % 8) + 1, i % 2 ? 9 : 12);
        return;
      }
      case "terminal":
        fill("#0d1117");
        line(th.done, 1, 1, 2);
        line(INK, 4, 1, 8);
        line(INK, 1, 3, 10);
        line(INK, 1, 5, 6);
        line(th.done, 1, 7, 2);
        if (t % 2 === 0) line("#e6edf3", 4, 7, 1);
        return;
      case "thinking":
        fill(SCREEN);
        for (let i = 0; i < 3; i++) line(i === t % 3 ? accent : INK, 5 + i * 3, 4, 1);
        return;
      case "idle":
        fill(SCREEN);
        line(INK, (t >> 2) % 14, 2 + ((t >> 3) % 5), 2);
        return;
      case "waiting":
        fill(SCREEN);
        glyph(ctx, "?", th.waiting, x + 7, y + 2);
        return;
      case "failed":
        fill(th.failed);
        if (this.reduced || t % 4 < 2) glyph(ctx, "!", "#ffffff", x + 7, y + 2);
        return;
      default:
        fill(furniture.x!);
    }
  }

  private bubble(x: number, y: number, kind: WaitingReason | "thinking" | undefined, th: Theme) {
    const { ctx } = this;
    const w = kind === "thinking" || kind === "idle" ? 9 : 7;
    ctx.fillStyle = th.wall;
    ctx.fillRect(x - 1, y - 1, w + 2, 9);
    ctx.fillStyle = th.bubble;
    ctx.fillRect(x, y, w, 7);
    ctx.fillRect(x + 2, y + 7, 2, 2); // tail
    const color = kind === "thinking" ? th.bubbleText : th.waiting;
    if (kind === "thinking" || kind === "idle") glyph(ctx, "dots", color, x + 2, y + 3);
    else if (kind === "question") glyph(ctx, "?", th.waiting, x + 2, y + 1);
    else glyph(ctx, "!", th.running, x + 3, y + 1);
  }

  private confetti(x: number, y: number, tick: number, th: Theme) {
    if (this.reduced) return;
    const colors = [th.running, th.waiting, th.done, th.review];
    for (let i = 0; i < 6; i++) {
      const h = (i * 37 + tick * 3) % 40;
      this.ctx.fillStyle = colors[i % colors.length]!;
      this.ctx.fillRect(x + 6 + ((i * 11) % 30), y + 4 + (h % 16), 1, 1);
    }
  }
}

function same(a: Point | null | undefined, b: Point | null | undefined) {
  return !!a && !!b && Math.abs(a.x - b.x) < 0.5 && Math.abs(a.y - b.y) < 0.5;
}

/** step moves p toward goal by at most d, along `first` before the other axis. */
function step(p: Point, goal: Point, d: number, first: "x" | "y") {
  const axes: ("x" | "y")[] = first === "x" ? ["x", "y"] : ["y", "x"];
  for (const k of axes) {
    const left = goal[k] - p[k];
    if (Math.abs(left) < 0.5) {
      p[k] = goal[k];
      continue;
    }
    const m = Math.sign(left) * Math.min(Math.abs(left), d);
    p[k] += m;
    d -= Math.abs(m);
    if (d <= 0) return;
  }
}

function ring(ctx: CanvasRenderingContext2D, x: number, y: number, w: number, h: number, color: string) {
  ctx.strokeStyle = color;
  ctx.lineWidth = 1;
  ctx.strokeRect(x + 0.5, y + 0.5, w - 1, h - 1);
}
