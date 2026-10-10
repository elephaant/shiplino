// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

"use client";

import { useMemo, useState } from "react";
import { Bar, BarChart, CartesianGrid, XAxis, YAxis } from "recharts";
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import {
  type ChartConfig,
  ChartContainer,
  ChartLegend,
  ChartLegendContent,
  ChartTooltip,
  ChartTooltipContent,
} from "@/components/ui/chart";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import type { Insights } from "@/lib/api";
import { agentName, agentVar, formatCost, formatDuration } from "@/lib/format";

type Metric = "cost" | "sessions" | "active";

const shortDate = (d: string) =>
  new Date(`${d}T12:00:00`).toLocaleDateString(undefined, { month: "short", day: "numeric" });

export function DailyChart({ data }: { data: Insights }) {
  // Spend is the default view, unless nothing in the period has a price.
  const [metric, setMetric] = useState<Metric>(data.totals.cost_usd > 0 ? "cost" : "sessions");
  const noSpend = metric === "cost" && data.totals.cost_usd === 0;
  const agents = data.agents.map((a) => a.key);

  const { rows, config } = useMemo(() => {
    const config: ChartConfig = {};
    if (metric === "cost") {
      for (const a of agents) config[a] = { label: agentName(a), color: agentVar(a) };
    } else {
      config.value = { label: metric === "sessions" ? "Sessions" : "Agents working", color: "var(--chart-1)" };
    }
    const rows = data.daily.map((d) => {
      const row: Record<string, string | number> = { date: d.date };
      if (metric === "cost") for (const a of agents) row[a] = d.cost_usd[a] ?? 0;
      else row.value = metric === "sessions" ? d.sessions : d.active_ms / 3_600_000;
      return row;
    });
    return { rows, config };
  }, [data, metric, agents]);

  const format = (v: number) =>
    metric === "cost" ? formatCost(v) : metric === "sessions" ? String(v) : formatDuration(v * 3_600_000);

  return (
    <Card className="h-full gap-4">
      <CardHeader>
        <CardTitle>Per day</CardTitle>
        <CardDescription>
          {metric === "cost"
            ? "Spend by agent"
            : metric === "sessions"
              ? "Sessions started"
              : "Time agents spent working"}
          , by the day each session started
        </CardDescription>
        <CardAction>
          <Tabs value={metric} onValueChange={(v) => setMetric(v as Metric)}>
            <TabsList className="h-8">
              <TabsTrigger value="cost" className="text-xs">
                Spend
              </TabsTrigger>
              <TabsTrigger value="sessions" className="text-xs">
                Sessions
              </TabsTrigger>
              <TabsTrigger value="active" className="text-xs">
                Time
              </TabsTrigger>
            </TabsList>
          </Tabs>
        </CardAction>
      </CardHeader>
      <CardContent className="px-2 sm:px-6">
        {noSpend ? (
          <div className="flex h-64 items-center justify-center rounded-lg border border-dashed px-6 text-center text-muted-foreground text-sm">
            No priced usage in this period: the agents didn't report token usage, or used models without list prices.
          </div>
        ) : (
          <ChartContainer config={config} className="aspect-auto h-64 w-full">
            <BarChart data={rows} margin={{ left: 0, right: 4 }}>
              <CartesianGrid vertical={false} />
              <XAxis
                dataKey="date"
                tickLine={false}
                axisLine={false}
                tickMargin={8}
                minTickGap={24}
                tickFormatter={shortDate}
              />
              <YAxis
                allowDecimals={metric !== "sessions"}
                tickLine={false}
                axisLine={false}
                width={52}
                tickFormatter={(v: number) =>
                  metric === "cost"
                    ? v < 1
                      ? `$${v.toFixed(2)}`
                      : `$${Math.round(v)}`
                    : metric === "active"
                      ? `${+v.toFixed(1)}h`
                      : String(v)
                }
              />
              <ChartTooltip
                cursor={{ fill: "var(--muted)", opacity: 0.5 }}
                content={
                  <ChartTooltipContent
                    labelFormatter={(d) => shortDate(String(d))}
                    formatter={(value, name) => (
                      <div className="flex w-full items-center justify-between gap-4">
                        <span className="text-muted-foreground">{config[String(name)]?.label ?? String(name)}</span>
                        <span className="font-mono tabular-nums">{format(Number(value))}</span>
                      </div>
                    )}
                  />
                }
              />
              {metric === "cost" && agents.length > 1 && <ChartLegend content={<ChartLegendContent />} />}
              {Object.keys(config).map((k, i, all) => (
                <Bar
                  key={k}
                  dataKey={k}
                  stackId="a"
                  fill={`var(--color-${k})`}
                  radius={i === all.length - 1 ? [3, 3, 0, 0] : 0}
                  maxBarSize={36}
                  isAnimationActive={false}
                />
              ))}
            </BarChart>
          </ChartContainer>
        )}
      </CardContent>
    </Card>
  );
}
