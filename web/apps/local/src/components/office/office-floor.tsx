"use client";

import { Bell } from "lucide-react";
import Link from "next/link";
import { useTheme } from "next-themes";
import { useEffect, useMemo, useRef, useState } from "react";
import { AgentDot } from "@/components/common/agent-dot";
import type { Session } from "@/lib/api";
import { agentName, agentVar, formatCost, formatDuration } from "@/lib/format";
import { type Desk, layout, type Pose, pose, poseLabel, rooms } from "@/lib/office";
import { type Actor, OfficeRenderer } from "./renderer";

/** Characters are this many CSS pixels per art pixel (fewer when the office is crowded). */
function scaleFor(width: number, people: number) {
  return width >= 1100 && people <= 24 ? 3 : 2;
}

function useReducedMotion() {
  const [reduced, setReduced] = useState(false);
  useEffect(() => {
    const q = window.matchMedia("(prefers-reduced-motion: reduce)");
    const on = () => setReduced(q.matches);
    on();
    q.addEventListener("change", on);
    return () => q.removeEventListener("change", on);
  }, []);
  return reduced;
}

export function OfficeFloor({
  sessions,
  names,
  now,
}: {
  sessions: Session[];
  names: Record<string, string>;
  now: number;
}) {
  const wrap = useRef<HTMLDivElement>(null);
  const canvas = useRef<HTMLCanvasElement>(null);
  const renderer = useRef<OfficeRenderer | null>(null);
  const [width, setWidth] = useState(0);
  const [hover, setHover] = useState<string | null>(null);
  const reduced = useReducedMotion();
  const { resolvedTheme } = useTheme();

  const byId = useMemo(() => new Map(sessions.map((s) => [s.id, s])), [sessions]);
  const groups = useMemo(() => rooms(sessions, now), [sessions, now]);
  const people = groups.reduce((n, r) => n + r.members.length, 0);
  const scale = scaleFor(width, people);
  const plan = useMemo(
    () => layout(groups, Math.floor(width / scale), (id) => byId.get(id)?.status === "waiting"),
    [groups, width, scale, byId],
  );
  const poses = useMemo(() => new Map(plan.desks.map((d) => [d.id, pose(byId.get(d.id)!, now)])), [plan, byId, now]);

  useEffect(() => {
    const el = wrap.current;
    if (!el) return;
    const ro = new ResizeObserver(([e]) => setWidth(Math.floor(e?.contentRect.width ?? 0)));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  useEffect(() => {
    if (!canvas.current) return;
    const r = new OfficeRenderer(canvas.current);
    renderer.current = r;
    return () => {
      r.destroy();
      renderer.current = null;
    };
  }, []);

  useEffect(() => renderer.current?.setReduced(reduced), [reduced]);
  // biome-ignore lint/correctness/useExhaustiveDependencies: re-read the tokens when the theme changes
  useEffect(() => {
    // next-themes switches the class before this effect; read after paint.
    const f = requestAnimationFrame(() => renderer.current?.refreshTheme());
    return () => cancelAnimationFrame(f);
  }, [resolvedTheme]);

  useEffect(() => {
    if (!width) return; // wait for the first measure: the layout depends on it
    const actors: Actor[] = plan.desks.map((d) => {
      const s = byId.get(d.id)!;
      return {
        id: d.id,
        desk: d,
        pose: poses.get(d.id)!,
        shirt: agentVar(s.agent),
        sub: !!s.parent_id,
        reason: s.waiting_reason,
      };
    });
    renderer.current?.setScene({ layout: plan, actors });
  }, [plan, poses, byId, width]);

  useEffect(() => renderer.current?.setHighlight(hover), [hover]);

  const box = (d: Desk, p: Pose) =>
    p === "waiting" && d.stand
      ? { x: d.stand.x - 7, y: d.stand.y - 32, w: 14, h: 34 }
      : { x: d.x, y: d.y, w: 40, h: 40 };
  const hovered = hover ? plan.desks.find((d) => d.id === hover) : undefined;

  return (
    <div ref={wrap} className="relative w-full">
      <section
        className="relative"
        style={{ width: plan.width * scale, height: plan.height * scale }}
        aria-label="Office floor"
      >
        <canvas
          ref={canvas}
          aria-hidden
          className="absolute inset-0 size-full"
          style={{ imageRendering: "pixelated" }}
        />
        {width > 0 &&
          plan.rooms.map((r) => {
            const members = groups.find((g) => g.id === r.id)?.members ?? [];
            const waiting = members.filter((id) => byId.get(id)?.status === "waiting").length;
            return (
              <div
                key={r.id}
                className="absolute flex max-w-full items-center gap-1.5 truncate rounded-sm bg-card/85 px-1 text-xs"
                style={{ left: (r.x + 4) * scale, top: (r.y + 4) * scale, maxWidth: (r.w - 24) * scale }}
              >
                <Link
                  href={`/board/?project=${encodeURIComponent(r.id)}`}
                  className="truncate font-medium hover:underline"
                  data-office-room={r.id}
                >
                  {names[r.id] ?? r.id.split("/").pop()}
                </Link>
                <span className="font-mono text-muted-foreground tabular-nums">{members.length}</span>
                {waiting > 0 && (
                  <span className="flex items-center gap-0.5 font-mono text-status-waiting tabular-nums">
                    <Bell className="size-3" aria-hidden />
                    {waiting}
                    <span className="sr-only">waiting on you</span>
                  </span>
                )}
              </div>
            );
          })}
        {width > 0 &&
          plan.desks.map((d) => {
            const s = byId.get(d.id)!;
            const p = poses.get(d.id)!;
            const b = box(d, p);
            return (
              <Link
                key={d.id}
                href={`/session/?id=${encodeURIComponent(d.id)}`}
                data-office-character={p}
                aria-label={`${s.title || "Untitled session"} (${agentName(s.agent)}${s.parent_id ? " subagent" : ""}): ${poseLabel(p, s.waiting_reason)}`}
                className="absolute rounded-sm focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
                style={{ left: b.x * scale, top: b.y * scale, width: b.w * scale, height: b.h * scale }}
                onMouseEnter={() => setHover(d.id)}
                onMouseLeave={() => setHover((h) => (h === d.id ? null : h))}
                onFocus={() => setHover(d.id)}
                onBlur={() => setHover((h) => (h === d.id ? null : h))}
              />
            );
          })}
        {hovered && (
          <HoverCard
            session={byId.get(hovered.id)!}
            pose={poses.get(hovered.id)!}
            box={box(hovered, poses.get(hovered.id)!)}
            scale={scale}
            width={plan.width * scale}
            now={now}
          />
        )}
      </section>
    </div>
  );
}

function HoverCard({
  session: s,
  pose: p,
  box,
  scale,
  width,
  now,
}: {
  session: Session;
  pose: Pose;
  box: { x: number; y: number; w: number; h: number };
  scale: number;
  width: number;
  now: number;
}) {
  const W = 256;
  const right = (box.x + box.w) * scale + 8;
  const left = right + W > width ? Math.max(0, box.x * scale - W - 8) : right;
  const end = s.ended_at ? Date.parse(s.ended_at) : Math.max(now, Date.parse(s.last_event_at));
  return (
    <div
      role="tooltip"
      className="pointer-events-none absolute z-10 flex flex-col gap-1 rounded-lg border bg-popover p-3 text-popover-foreground text-sm shadow-md"
      style={{ left, top: box.y * scale, width: W }}
    >
      <p className="flex items-center gap-1.5 text-muted-foreground text-xs">
        <AgentDot agent={s.agent} />
        {agentName(s.agent)}
        {s.parent_id && <span>· subagent</span>}
      </p>
      <p className="line-clamp-2 font-medium">{s.title || "Untitled session"}</p>
      <p className="text-xs">{poseLabel(p, s.waiting_reason)}</p>
      {s.now_doing && <p className="truncate font-mono text-muted-foreground text-xs">{s.now_doing}</p>}
      <p className="flex gap-3 font-mono text-muted-foreground text-xs tabular-nums">
        <span>{formatDuration(end - Date.parse(s.started_at))}</span>
        <span>{formatCost(s.best_cost_usd)}</span>
        {(s.lines_added > 0 || s.lines_removed > 0) && (
          <span>
            +{s.lines_added} −{s.lines_removed}
          </span>
        )}
      </p>
      <p className="text-muted-foreground text-xs">Click to open the session.</p>
    </div>
  );
}
