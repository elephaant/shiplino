"use client";

import { cn } from "cn";
import { Bot, Hand, Moon, Play } from "lucide-react";
import Link from "next/link";
import { useEffect, useMemo, useRef, useState } from "react";
import { AgentDot } from "@/components/common/agent-dot";
import type { Segment, SegmentState, Timeline, TimelineRow } from "@/lib/api";
import { agentName, agentVar, formatCost, formatDuration } from "@/lib/format";

export type Zoom = "hour" | "day" | "week";

const HOUR = 3_600_000;

// Label column + track; the header, the rows and the grid-line overlay share it.
const COLS = "grid grid-cols-[clamp(7.5rem,30%,16rem)_1fr]";

/** Window length and tick spacing per zoom level. */
export const zooms: Record<Zoom, { label: string; span: number; tick: number }> = {
  hour: { label: "Hour", span: HOUR, tick: 10 * 60_000 },
  day: { label: "Day", span: 24 * HOUR, tick: 2 * HOUR },
  week: { label: "Week", span: 7 * 24 * HOUR, tick: 24 * HOUR },
};

const time = (t: number) => new Date(t).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false });
const day = (t: number) => new Date(t).toLocaleDateString([], { weekday: "short", day: "numeric", month: "short" });

/** Tick positions aligned to local clock boundaries. */
function ticks(from: number, to: number, zoom: Zoom): { at: number; label: string }[] {
  const { tick } = zooms[zoom];
  const d = new Date(from);
  if (zoom === "week") d.setHours(0, 0, 0, 0);
  else {
    d.setMinutes(0, 0, 0);
    if (zoom === "day") d.setHours(d.getHours() - (d.getHours() % 2));
  }
  const out: { at: number; label: string }[] = [];
  for (let t = d.getTime(); t <= to; ) {
    if (t >= from) out.push({ at: t, label: zoom === "week" ? day(t) : time(t) });
    if (zoom === "week") {
      const n = new Date(t);
      n.setDate(n.getDate() + 1);
      t = n.getTime(); // calendar days, so DST changes stay on midnight
    } else t += tick;
  }
  return out;
}

const stateText: Record<SegmentState, string> = { running: "Running", waiting: "Waiting on you", idle: "Idle" };
const stateIcon = { running: Play, waiting: Hand, idle: Moon } as const;

function totals(row: TimelineRow) {
  const t: Record<SegmentState, number> = { running: 0, waiting: 0, idle: 0 };
  for (const s of row.segments) t[s.state] += Date.parse(s.end) - Date.parse(s.start);
  return t;
}

function rowLabel(row: TimelineRow) {
  return row.parent_id ? row.actor_type || "Subagent" : row.title || row.id;
}

function SegmentBar({ seg, row, from, span }: { seg: Segment; row: TimelineRow; from: number; span: number }) {
  const start = Date.parse(seg.start);
  const end = Date.parse(seg.end);
  const left = ((start - from) / span) * 100;
  const width = ((end - start) / span) * 100;
  const color = agentVar(row.agent);
  const sub = !!row.parent_id;
  const style: React.CSSProperties = { left: `${left}%`, width: `${width}%` };
  if (seg.state === "idle") {
    return (
      <span
        className="absolute top-1/2 h-0.5 -translate-y-1/2 opacity-40"
        style={{ ...style, backgroundColor: color }}
      />
    );
  }
  if (seg.state === "waiting") {
    style.backgroundImage =
      "repeating-linear-gradient(135deg, var(--status-waiting) 0 3px, color-mix(in oklch, var(--status-waiting) 35%, transparent) 3px 6px)";
  } else style.backgroundColor = color;
  return (
    <span
      className={cn(
        "absolute top-1/2 min-w-0.5 -translate-y-1/2 rounded-[3px]",
        sub ? "h-2.5" : "h-3.5",
        seg.state === "waiting" && "ring-1 ring-status-waiting ring-inset",
      )}
      style={style}
    />
  );
}

interface Tip {
  row: TimelineRow;
  seg?: Segment;
  x: number;
  y: number;
}

function Tooltip({ tip, now }: { tip: Tip; now: number }) {
  const { row, seg } = tip;
  const t = totals(row);
  const Icon = seg ? stateIcon[seg.state] : null;
  return (
    <div
      role="tooltip"
      className="pointer-events-none fixed z-50 w-64 rounded-md border bg-popover p-2.5 text-popover-foreground text-xs shadow-md"
      style={{
        left: Math.min(tip.x + 12, (typeof window === "undefined" ? 9999 : window.innerWidth) - 272),
        top: tip.y + 14,
      }}
    >
      <p className="flex items-center gap-1.5 font-medium text-sm">
        <AgentDot agent={row.agent} />
        <span className="truncate">{rowLabel(row)}</span>
      </p>
      <p className="mt-0.5 text-muted-foreground">
        {agentName(row.agent)} · {row.status === "waiting" ? "waiting on you" : row.status}
        {row.cost_usd > 0 && ` · ${formatCost(row.cost_usd)}`}
      </p>
      {seg && Icon && (
        <p className="mt-2 flex items-center gap-1.5">
          <Icon className={cn("size-3.5", seg.state === "waiting" && "text-status-waiting")} aria-hidden />
          <span className="font-medium">{stateText[seg.state]}</span>
          <span className="ml-auto font-mono tabular-nums text-muted-foreground">
            {time(Date.parse(seg.start))}–{Date.parse(seg.end) >= now - 5000 ? "now" : time(Date.parse(seg.end))}
          </span>
        </p>
      )}
      {seg && (
        <p className="font-mono tabular-nums text-muted-foreground">
          {formatDuration(Date.parse(seg.end) - Date.parse(seg.start))}
        </p>
      )}
      <p className="mt-2 grid grid-cols-3 gap-1 border-t pt-2 font-mono tabular-nums">
        <span>
          <span className="block font-sans text-muted-foreground">Running</span>
          {formatDuration(t.running)}
        </span>
        <span>
          <span className="block font-sans text-muted-foreground">Waiting</span>
          {formatDuration(t.waiting)}
        </span>
        <span>
          <span className="block font-sans text-muted-foreground">Idle</span>
          {formatDuration(t.idle)}
        </span>
      </p>
    </div>
  );
}

export function Legend() {
  return (
    <div className="flex flex-wrap items-center gap-4 text-muted-foreground text-xs">
      <span className="flex items-center gap-1.5">
        <Play className="size-3.5" aria-hidden />
        <span className="h-2.5 w-5 rounded-[3px] bg-agent-claude" aria-hidden />
        Running (agent color)
      </span>
      <span className="flex items-center gap-1.5">
        <Hand className="size-3.5 text-status-waiting" aria-hidden />
        <span
          className="h-2.5 w-5 rounded-[3px] ring-1 ring-status-waiting ring-inset"
          style={{
            backgroundImage:
              "repeating-linear-gradient(135deg, var(--status-waiting) 0 3px, color-mix(in oklch, var(--status-waiting) 35%, transparent) 3px 6px)",
          }}
          aria-hidden
        />
        Waiting on you
      </span>
      <span className="flex items-center gap-1.5">
        <Moon className="size-3.5" aria-hidden />
        <span className="h-0.5 w-5 bg-muted-foreground/60" aria-hidden />
        Idle between turns
      </span>
    </div>
  );
}

/** A Gantt chart of sessions over [from, to): one row per session, subagents indented below. */
export function Gantt({ data, zoom, now }: { data: Timeline; zoom: Zoom; now: number }) {
  const from = Date.parse(data.from);
  const to = Date.parse(data.to);
  const span = to - from;
  const marks = useMemo(() => ticks(from, to, zoom), [from, to, zoom]);
  const [tip, setTip] = useState<Tip | null>(null);
  const body = useRef<HTMLDivElement>(null);
  const axis = useRef<HTMLDivElement>(null);
  const [axisWidth, setAxisWidth] = useState(800);
  useEffect(() => {
    if (!axis.current) return;
    const ro = new ResizeObserver(([e]) => e && setAxisWidth(e.contentRect.width));
    ro.observe(axis.current);
    return () => ro.disconnect();
  }, []);
  // Skip labels on narrow screens so they never overlap (~80px each).
  const labelEvery = Math.max(1, Math.ceil((marks.length * 80) / Math.max(axisWidth, 1)));
  const nowPct = ((now - from) / span) * 100;

  const hover = (row: TimelineRow, e: React.PointerEvent<HTMLElement>) => {
    const r = e.currentTarget.getBoundingClientRect();
    const at = from + ((e.clientX - r.left) / r.width) * span;
    const seg = row.segments.find((s) => Date.parse(s.start) <= at && at < Date.parse(s.end));
    setTip({ row, seg, x: e.clientX, y: e.clientY });
  };

  // j/k and the arrow keys move between rows; Enter opens the session.
  const onKeyDown = (e: React.KeyboardEvent) => {
    const keys: Record<string, number> = { j: 1, ArrowDown: 1, k: -1, ArrowUp: -1 };
    const step = keys[e.key];
    if (!step || !body.current) return;
    const rows = Array.from(body.current.querySelectorAll<HTMLElement>("[data-row]"));
    const i = rows.indexOf(document.activeElement as HTMLElement);
    const next = rows[Math.max(0, Math.min(rows.length - 1, i + step))];
    if (next) {
      e.preventDefault();
      next.focus();
    }
  };

  return (
    <div className="overflow-hidden rounded-lg border bg-card text-sm shadow-xs">
      <div className={cn(COLS, "border-b bg-muted/40")}>
        <div className="px-3 py-2 font-medium text-muted-foreground text-xs">Session</div>
        <div ref={axis} className="relative h-8" aria-hidden>
          {marks.map((m, i) => (
            <span
              hidden={i % labelEvery !== 0}
              key={m.at}
              className="absolute top-2 whitespace-nowrap pl-1.5 font-mono text-[11px] text-muted-foreground tabular-nums"
              style={{ left: `${((m.at - from) / span) * 100}%` }}
            >
              {m.label}
            </span>
          ))}
        </div>
      </div>

      {/* biome-ignore lint/a11y/noStaticElementInteractions: delegates j/k to the row links inside */}
      <div ref={body} className="relative" onKeyDown={onKeyDown} onPointerLeave={() => setTip(null)}>
        {/* Same columns as the rows, so the grid lines line up with the bars. */}
        <div className={cn("pointer-events-none absolute inset-0", COLS)} aria-hidden>
          <span />
          <span className="relative">
            {marks.map((m) => (
              <span
                key={m.at}
                className="absolute inset-y-0 w-px bg-border/70"
                style={{ left: `${((m.at - from) / span) * 100}%` }}
              />
            ))}
            {nowPct >= 0 && nowPct <= 100 && (
              <span className="absolute inset-y-0 z-10 w-px bg-primary" style={{ left: `${nowPct}%` }}>
                <span className="absolute -top-px -left-0.75 size-1.75 rounded-full bg-primary" />
              </span>
            )}
          </span>
        </div>

        {data.rows.map((row) => {
          const t = totals(row);
          const sub = !!row.parent_id;
          return (
            <Link
              key={row.id}
              data-row
              href={`/session/?id=${encodeURIComponent(row.id)}`}
              aria-label={`${rowLabel(row)}, ${agentName(row.agent)}, ${row.status}. Running ${formatDuration(t.running)}, waiting ${formatDuration(t.waiting)}.`}
              onFocus={(e) => {
                const r = e.currentTarget.getBoundingClientRect();
                setTip({ row, x: r.left + 260, y: r.top + r.height / 2 });
              }}
              onBlur={() => setTip(null)}
              className={cn(
                COLS,
                "border-b last:border-b-0 hover:bg-accent/30 focus-visible:bg-accent/40 focus-visible:outline-2 focus-visible:outline-ring focus-visible:-outline-offset-2",
                sub ? "h-8" : "h-10",
              )}
            >
              <span
                className="flex min-w-0 items-center gap-2 pr-2"
                style={{ paddingLeft: `${0.75 + row.depth * 1}rem` }}
              >
                {sub ? (
                  <Bot className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
                ) : (
                  <AgentDot agent={row.agent} />
                )}
                <span className={cn("truncate", sub ? "text-muted-foreground text-xs" : "font-medium")}>
                  {rowLabel(row)}
                </span>
                {row.status === "waiting" && (
                  <Hand className="size-3.5 shrink-0 text-status-waiting" aria-label="waiting on you" />
                )}
              </span>
              <span className="relative" onPointerMove={(e) => hover(row, e)} onPointerLeave={() => setTip(null)}>
                {row.segments.map((s) => (
                  <SegmentBar key={s.start} seg={s} row={row} from={from} span={span} />
                ))}
              </span>
            </Link>
          );
        })}
      </div>
      {tip && <Tooltip tip={tip} now={now} />}
    </div>
  );
}
