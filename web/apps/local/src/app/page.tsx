// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

"use client";

import { Hand, Radio } from "lucide-react";
import Link from "next/link";
import { useEffect, useState } from "react";
import { AgentDot } from "@/components/common/agent-dot";
import { Empty } from "@/components/common/empty";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { api, type Overview } from "@/lib/api";
import { formatAgo, formatCost } from "@/lib/format";
import { useLive } from "@/lib/live";

const cols = [
  ["running", "Running"],
  ["waiting", "Waiting"],
  ["review", "Review"],
  ["done", "Done"],
] as const;

export default function OverviewPage() {
  const version = useLive((s) => s.version);
  const [data, setData] = useState<Overview | null>(null);
  const [error, setError] = useState<string | null>(null);

  // biome-ignore lint/correctness/useExhaustiveDependencies: refetch on live changes
  useEffect(() => {
    api<Overview>("/api/v1/overview")
      .then((d) => {
        setData(d);
        setError(null);
      })
      .catch((e: Error) => setError(e.message));
  }, [version]);

  if (error && !data) {
    return (
      <Empty icon={Radio} title="Can't reach the Shiplino daemon">
        Run <code className="font-mono">shiplino doctor</code> in a terminal.
      </Empty>
    );
  }
  if (!data) return <Skeleton className="h-64 w-full" />;

  return (
    <div className="flex flex-col gap-6">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">All projects</h1>
        <p className="text-sm text-muted-foreground">This sprint, across every repo your agents work in.</p>
      </div>

      {data.needs_you.length > 0 && (
        <Card id="needs-you" className="gap-2 border-status-waiting/60 py-4">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <Hand className="size-4 text-status-waiting" aria-hidden /> Needs you ({data.needs_you.length})
            </CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col divide-y">
            {data.needs_you.map((n) => (
              <Link
                key={n.session_id}
                href={`/board/?project=${encodeURIComponent(n.project_id)}`}
                className="flex items-center gap-3 py-2 text-sm hover:bg-accent/50"
              >
                <AgentDot agent={n.agent} />
                <span className="font-medium">{n.title || n.root_id}</span>
                <span className="truncate text-muted-foreground">{n.message}</span>
                <span className="ml-auto shrink-0 font-mono text-xs text-muted-foreground">
                  waiting {formatAgo(n.since).replace(" ago", "")}
                </span>
              </Link>
            ))}
          </CardContent>
        </Card>
      )}

      {data.projects.length === 0 ? (
        <Empty icon={Radio} title="Start any agent. It'll appear here within a second.">
          Shiplino is listening. No tokens are used.
        </Empty>
      ) : (
        <Card className="gap-0 py-0">
          <CardContent className="overflow-x-auto p-0">
            <table className="w-full text-sm">
              <thead className="text-left text-xs text-muted-foreground">
                <tr className="border-b">
                  <th className="px-4 py-2 font-medium">Project</th>
                  {cols.map(([, label]) => (
                    <th key={label} className="px-3 py-2 text-right font-medium">
                      {label}
                    </th>
                  ))}
                  <th className="px-3 py-2 text-right font-medium">Sprint cost</th>
                  <th className="px-4 py-2 text-right font-medium">Last activity</th>
                </tr>
              </thead>
              <tbody>
                {data.projects.map((p) => (
                  <tr key={p.id} className="border-b last:border-0 hover:bg-accent/40">
                    <td className="px-4 py-2.5">
                      <Link
                        href={`/board/?project=${encodeURIComponent(p.id)}`}
                        className="font-medium hover:underline"
                      >
                        {p.name}
                      </Link>
                      <div className="text-xs text-muted-foreground">{p.sprint.name}</div>
                    </td>
                    {cols.map(([id]) => (
                      <td key={id} className="px-3 py-2.5 text-right font-mono tabular-nums">
                        {p.columns[id] ? (
                          id === "waiting" ? (
                            <Badge className="bg-status-waiting text-foreground">{p.columns[id]}</Badge>
                          ) : (
                            p.columns[id]
                          )
                        ) : (
                          <span className="text-muted-foreground">—</span>
                        )}
                        {id === "running" && p.running_subagents > 0 && (
                          <span className="ml-1 text-xs text-muted-foreground">+{p.running_subagents} sub</span>
                        )}
                      </td>
                    ))}
                    <td className="px-3 py-2.5 text-right font-mono tabular-nums">{formatCost(p.sprint_cost_usd)}</td>
                    <td className="px-4 py-2.5 text-right text-muted-foreground">{formatAgo(p.last_seen)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </CardContent>
        </Card>
      )}
    </div>
  );
}
