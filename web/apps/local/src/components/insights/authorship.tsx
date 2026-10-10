"use client";

import { EvidenceBadge } from "@shiplino/ui/evidence";
import { Bar, BarChart, CartesianGrid, XAxis, YAxis } from "recharts";
import { AgentDot } from "@/components/common/agent-dot";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import {
  type ChartConfig,
  ChartContainer,
  ChartLegend,
  ChartLegendContent,
  ChartTooltip,
  ChartTooltipContent,
} from "@/components/ui/chart";
import type { Insights, LineSplit } from "@/lib/api";
import { authorshipEvidence } from "@/lib/evidence";
import { agentName } from "@/lib/format";

// Agent lines take the primary hue, other lines a contrasting blue, and
// unknown lines a neutral grey; the legend and tooltip name each one.
const config = {
  agent_lines: { label: "By agents", color: "var(--primary)" },
  human_lines: { label: "Other", color: "var(--chart-3)" },
  unknown_lines: { label: "Unknown", color: "var(--muted-foreground)" },
} satisfies ChartConfig;
const keys = Object.keys(config) as (keyof typeof config)[];

const shortDate = (d: string) =>
  new Date(`${d}T12:00:00`).toLocaleDateString(undefined, { month: "short", day: "numeric" });

/** The agents' share of the lines whose author is known, or null when none is. */
function share(r: LineSplit): number | null {
  const known = r.agent_lines + r.human_lines;
  return known > 0 ? r.agent_lines / known : null;
}

const pct = (v: number | null) => (v == null ? "—" : `${Math.round(v * 100)}%`);

function SplitBar({ r }: { r: LineSplit }) {
  const total = r.agent_lines + r.human_lines + r.unknown_lines;
  if (!total) return <div className="h-1.5 w-full rounded-full bg-muted" aria-hidden />;
  return (
    <div className="flex h-1.5 w-full gap-0.5 overflow-hidden rounded-full bg-muted" aria-hidden>
      {keys.map((k) =>
        r[k] > 0 ? <div key={k} style={{ width: `${(r[k] / total) * 100}%`, background: config[k].color }} /> : null,
      )}
    </div>
  );
}

function Rows({ title, rows, agents }: { title: string; rows: LineSplit[]; agents?: boolean }) {
  if (!rows.length) return null;
  return (
    <div className="flex flex-col gap-3">
      <h3 className="font-medium text-muted-foreground text-xs">{title}</h3>
      {rows.map((r) => (
        <div key={r.key} className="flex flex-col gap-1.5">
          <div className="flex items-center gap-2 text-sm">
            {agents && <AgentDot agent={r.key} />}
            <span className="min-w-0 truncate font-medium">{agents ? agentName(r.key) : r.name || r.key}</span>
            <span className="ml-auto font-mono tabular-nums">{pct(share(r))}</span>
          </div>
          <SplitBar r={r} />
          <div className="flex flex-wrap gap-x-3 font-mono text-muted-foreground text-xs tabular-nums">
            <span>{r.commits} commits</span>
            <span>
              {r.agent_lines} of {r.agent_lines + r.human_lines} lines by agents
            </span>
            {r.unknown_lines > 0 && <span>{r.unknown_lines} unknown</span>}
          </div>
        </div>
      ))}
    </div>
  );
}

/** Committed lines written by agents, per day, agent and project. */
export function Authorship({ data }: { data: Insights }) {
  const a = data.authorship;
  const t = a.totals;
  const onlyUnknown = t.commits > 0 && t.agent_lines + t.human_lines === 0;
  return (
    <Card className="gap-4">
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          Committed lines by agents
          {t.commits > 0 && (
            <EvidenceBadge
              {...authorshipEvidence(onlyUnknown ? "unknown" : t.unknown_lines ? "partial" : "observed")}
            />
          )}
        </CardTitle>
        <CardDescription>
          {t.commits === 0
            ? "No commits linked to agent sessions in this period."
            : `${pct(share(t))} of known committed lines came from agent edits: ${t.agent_lines} of ${t.agent_lines + t.human_lines} in ${t.commits} commits${t.unknown_lines ? `, ${t.unknown_lines} lines unknown` : ""}.`}
          {onlyUnknown && " Splitting lines needs capture level full: the diffs of agent edits."}
        </CardDescription>
      </CardHeader>
      {t.commits > 0 && (
        <CardContent className="flex flex-col gap-6">
          <ChartContainer config={config} className="aspect-auto h-56 w-full">
            <BarChart data={a.daily} margin={{ left: 0, right: 4 }}>
              <CartesianGrid vertical={false} />
              <XAxis
                dataKey="key"
                tickLine={false}
                axisLine={false}
                tickMargin={8}
                minTickGap={24}
                tickFormatter={shortDate}
              />
              <YAxis allowDecimals={false} tickLine={false} axisLine={false} width={44} />
              <ChartTooltip
                cursor={{ fill: "var(--muted)", opacity: 0.5 }}
                content={<ChartTooltipContent labelFormatter={(d) => shortDate(String(d))} />}
              />
              <ChartLegend content={<ChartLegendContent />} />
              {keys.map((k, i) => (
                <Bar
                  key={k}
                  dataKey={k}
                  stackId="a"
                  fill={`var(--color-${k})`}
                  radius={i === keys.length - 1 ? [3, 3, 0, 0] : 0}
                  maxBarSize={36}
                  isAnimationActive={false}
                />
              ))}
            </BarChart>
          </ChartContainer>
          <div className="grid grid-cols-1 gap-6 md:grid-cols-2">
            <Rows title="By agent" rows={a.agents} agents />
            <Rows title="By project" rows={a.projects} />
          </div>
        </CardContent>
      )}
    </Card>
  );
}
