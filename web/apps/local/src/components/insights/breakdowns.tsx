// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

import { Info } from "lucide-react";
import Link from "next/link";
import { AgentDot } from "@/components/common/agent-dot";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import type { InsightRow, Insights } from "@/lib/api";
import { agentName, formatCost, formatDuration, formatTokens } from "@/lib/format";

function Share({ value, max }: { value: number; max: number }) {
  const pct = max > 0 && value > 0 ? Math.max(2, (value / max) * 100) : 0;
  return (
    <div className="h-1.5 w-full rounded-full bg-muted" aria-hidden>
      <div className="h-full rounded-full bg-primary/70" style={{ width: `${pct}%` }} />
    </div>
  );
}

export function ByAgent({ rows }: { rows: InsightRow[] }) {
  const max = Math.max(...rows.map((r) => r.cost_usd), 0);
  return (
    <Card className="gap-3">
      <CardHeader>
        <CardTitle>By agent</CardTitle>
        <CardDescription>Spend, work time and tokens (including subagents)</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {rows.map((r) => (
          <div key={r.key} className="flex flex-col gap-1.5">
            <div className="flex items-center gap-2 text-sm">
              <AgentDot agent={r.key} />
              <span className="font-medium">{agentName(r.key)}</span>
              <span className="ml-auto font-mono tabular-nums">{formatCost(r.cost_usd)}</span>
            </div>
            <Share value={r.cost_usd} max={max} />
            <div className="flex gap-3 font-mono text-muted-foreground text-xs tabular-nums">
              <span>{r.sessions} sessions</span>
              <span>{formatDuration(r.active_ms)} working</span>
              <span>
                {formatTokens(r.input_tokens + r.cache_read_tokens)} in · {formatTokens(r.output_tokens)} out
              </span>
            </div>
          </div>
        ))}
      </CardContent>
    </Card>
  );
}

const sourceText: Record<string, string> = {
  reported: "reported by the agent itself",
  computed: "computed from token usage × list prices",
  unpriced: "with token counts but no price for the model",
  none: "without cost data: the agent recorded no token usage",
};

export function CostSources({ sources }: { sources: Record<string, number> }) {
  const total = Object.values(sources).reduce((a, b) => a + b, 0);
  if (!total) return null;
  return (
    <Card className="gap-3">
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-sm">
          <Info className="size-4 text-muted-foreground" aria-hidden /> Where the dollars come from
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-1.5 text-sm">
        {(["reported", "computed", "unpriced", "none"] as const)
          .filter((k) => sources[k])
          .map((k) => (
            <div key={k} className="flex gap-2">
              <span className="w-10 shrink-0 text-right font-mono tabular-nums">{sources[k]}</span>
              <span className="text-muted-foreground">
                session{sources[k] === 1 ? "" : "s"} {sourceText[k]}
              </span>
            </div>
          ))}
        <p className="pt-1 text-muted-foreground text-xs">
          Figures are list-price estimates, not your bill: subscriptions and discounts aren't visible here.
        </p>
      </CardContent>
    </Card>
  );
}

export function ByProject({ rows }: { rows: InsightRow[] }) {
  return (
    <Card className="h-full gap-3">
      <CardHeader>
        <CardTitle>By project</CardTitle>
      </CardHeader>
      <CardContent>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Project</TableHead>
              <TableHead className="text-right">Sessions</TableHead>
              <TableHead className="text-right">Working</TableHead>
              <TableHead className="text-right">Lines</TableHead>
              <TableHead className="text-right">Spend</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((r) => (
              <TableRow key={r.key || "unsorted"}>
                <TableCell className="max-w-48 truncate font-medium">
                  {r.key ? (
                    <Link href={`/board?project=${encodeURIComponent(r.key)}`} className="hover:underline">
                      {r.name || r.key}
                    </Link>
                  ) : (
                    <span className="text-muted-foreground">{r.name}</span>
                  )}
                </TableCell>
                <TableCell className="text-right font-mono tabular-nums">{r.sessions}</TableCell>
                <TableCell className="text-right font-mono tabular-nums">{formatDuration(r.active_ms)}</TableCell>
                <TableCell className="text-right font-mono text-xs tabular-nums">
                  <span className="text-status-done">+{r.lines_added}</span>{" "}
                  <span className="text-status-failed">−{r.lines_removed}</span>
                </TableCell>
                <TableCell className="text-right font-mono tabular-nums">{formatCost(r.cost_usd)}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </CardContent>
    </Card>
  );
}

export function Models({ rows }: { rows: InsightRow[] }) {
  if (rows.length === 0) return null;
  return (
    <Card className="gap-3">
      <CardHeader>
        <CardTitle>Models</CardTitle>
        <CardDescription>Tokens per model, cost computed per response</CardDescription>
      </CardHeader>
      <CardContent>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Model</TableHead>
              <TableHead className="text-right">Input</TableHead>
              <TableHead className="text-right">Cached</TableHead>
              <TableHead className="text-right">Output</TableHead>
              <TableHead className="text-right">Cost</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((r) => (
              <TableRow key={r.key}>
                <TableCell className="max-w-44 truncate font-mono text-xs">{r.key}</TableCell>
                <TableCell className="text-right font-mono tabular-nums">{formatTokens(r.input_tokens)}</TableCell>
                <TableCell className="text-right font-mono tabular-nums">{formatTokens(r.cache_read_tokens)}</TableCell>
                <TableCell className="text-right font-mono tabular-nums">{formatTokens(r.output_tokens)}</TableCell>
                <TableCell className="text-right font-mono tabular-nums">{formatCost(r.cost_usd)}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </CardContent>
    </Card>
  );
}

export function Tools({ data }: { data: Insights }) {
  const max = Math.max(...data.tools.map((t) => t.calls), 0);
  const { tool_calls: calls, tool_errors: errors } = data.totals;
  return (
    <Card className="h-full gap-3">
      <CardHeader>
        <CardTitle>Top tools</CardTitle>
        <CardDescription>
          {calls.toLocaleString()} calls
          {calls > 0 && ` · ${((errors / calls) * 100).toFixed(1)}% failed`}
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-2.5">
        {data.tools.length === 0 && <p className="text-muted-foreground text-sm">No tool calls in this period.</p>}
        {data.tools.map((t) => (
          <div key={t.tool} className="flex items-center gap-3 text-sm">
            <span className="w-32 shrink-0 truncate font-mono text-xs" title={t.tool}>
              {t.tool}
            </span>
            <Share value={t.calls} max={max} />
            <span className="w-12 shrink-0 text-right font-mono text-xs tabular-nums">{t.calls}</span>
          </div>
        ))}
      </CardContent>
    </Card>
  );
}
