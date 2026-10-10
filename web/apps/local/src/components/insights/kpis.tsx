import { ArrowDownRight, ArrowUpRight, Minus } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import type { InsightTotals } from "@/lib/api";
import { change, formatCost, formatDuration } from "@/lib/format";
import { API_EQUIVALENT } from "@/lib/limits";

function Delta({ cur, prev, days, goodWhenDown }: { cur: number; prev: number; days: number; goodWhenDown?: boolean }) {
  const pct = change(cur, prev);
  if (pct === undefined) return null; // nothing to compare with yet
  const flat = Math.abs(pct) < 1;
  const Icon = flat ? Minus : pct > 0 ? ArrowUpRight : ArrowDownRight;
  // Only waiting time has an obvious "good" direction; others stay neutral.
  const tone = goodWhenDown && !flat ? (pct < 0 ? "text-status-done" : "text-status-failed") : "text-muted-foreground";
  return (
    <span
      className={`inline-flex shrink-0 items-center gap-0.5 font-mono text-xs tabular-nums ${tone}`}
      title={`Compared with the previous ${days} days`}
    >
      <Icon className="size-3.5" aria-hidden />
      {flat ? "same" : `${Math.abs(pct).toFixed(0)}%`}
    </span>
  );
}

function Tile({
  title,
  value,
  sub,
  hint,
  delta,
}: {
  title: string;
  value: string;
  sub?: string;
  hint?: string;
  delta: React.ReactNode;
}) {
  return (
    <Card className="gap-2 rounded-none py-4 shadow-none ring-0">
      <CardHeader className="px-4">
        <CardTitle className="font-normal text-muted-foreground text-sm">{title}</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-1.5 px-4">
        <div className="flex items-baseline justify-between gap-2">
          <div className="truncate font-mono text-2xl leading-none tracking-tight tabular-nums">{value}</div>
          {delta}
        </div>
        {sub && (
          <div className="truncate text-muted-foreground text-xs" title={hint}>
            {sub}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

export function Kpis({
  t,
  p,
  days,
  apiEquivalent,
}: {
  t: InsightTotals;
  p: InsightTotals;
  days: number;
  /** Some agents run on a flat-rate plan: $ is what it would cost, not a bill. */
  apiEquivalent?: boolean;
}) {
  return (
    <div className="overflow-hidden rounded-xl border bg-card">
      <div className="grid divide-y sm:grid-cols-2 sm:divide-x lg:grid-cols-5 lg:divide-y-0">
        <Tile
          title="Spend"
          value={formatCost(t.cost_usd)}
          sub={apiEquivalent ? "API-equivalent, at list prices" : "at list prices"}
          hint={apiEquivalent ? API_EQUIVALENT : undefined}
          delta={<Delta cur={t.cost_usd} prev={p.cost_usd} days={days} />}
        />
        <Tile
          title="Sessions"
          value={String(t.sessions)}
          sub={`${t.turns} prompts`}
          delta={<Delta cur={t.sessions} prev={p.sessions} days={days} />}
        />
        <Tile
          title="Agents working"
          value={formatDuration(t.active_ms)}
          sub="time in turns"
          delta={<Delta cur={t.active_ms} prev={p.active_ms} days={days} />}
        />
        <Tile
          title="Waiting on you"
          value={formatDuration(t.waiting_ms)}
          sub="blocked on your answers"
          delta={<Delta cur={t.waiting_ms} prev={p.waiting_ms} days={days} goodWhenDown />}
        />
        <Tile
          title="Code changed"
          value={`+${t.lines_added.toLocaleString()} −${t.lines_removed.toLocaleString()}`}
          sub={`${t.files} files · ${t.commits} commits${t.prs ? ` · ${t.prs} PRs` : ""}`}
          delta={<Delta cur={t.lines_added + t.lines_removed} prev={p.lines_added + p.lines_removed} days={days} />}
        />
      </div>
    </div>
  );
}
