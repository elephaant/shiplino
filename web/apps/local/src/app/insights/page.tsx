"use client";

import { ChartColumn, Download, Radio } from "lucide-react";
import { useEffect, useState } from "react";
import { Empty } from "@/components/common/empty";
import { ByAgent, ByProject, CostSources, Models, Tools } from "@/components/insights/breakdowns";
import { DailyChart } from "@/components/insights/daily-chart";
import { Kpis } from "@/components/insights/kpis";
import { Button } from "@/components/ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { API_BASE, api, type Insights, type ProjectSummary } from "@/lib/api";
import { useLive } from "@/lib/live";

const ranges = [
  ["7", "Last 7 days"],
  ["30", "Last 30 days"],
  ["90", "Last 90 days"],
] as const;

export default function InsightsPage() {
  const version = useLive((s) => s.version);
  const [days, setDays] = useState("30");
  const [project, setProject] = useState("all");
  const [projects, setProjects] = useState<ProjectSummary[]>([]);
  const [data, setData] = useState<Insights | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [tick, setTick] = useState(0);

  useEffect(() => {
    api<{ projects: ProjectSummary[] }>("/api/v1/projects")
      .then((r) => setProjects(r.projects))
      .catch(() => {});
  }, []);

  // Live changes arrive many times a second while agents work; aggregates
  // only need a refresh every few seconds.
  // biome-ignore lint/correctness/useExhaustiveDependencies: throttled refresh on live changes
  useEffect(() => {
    const t = setTimeout(() => setTick((n) => n + 1), 5000);
    return () => clearTimeout(t);
  }, [version]);

  // biome-ignore lint/correctness/useExhaustiveDependencies: tick triggers a refresh
  useEffect(() => {
    const p = project === "all" ? "" : `&project=${encodeURIComponent(project)}`;
    api<Insights>(`/api/v1/insights?days=${days}${p}`)
      .then((d) => {
        setData(d);
        setError(null);
      })
      .catch((e: Error) => setError(e.message));
  }, [days, project, tick]);

  if (error && !data) {
    return (
      <Empty icon={Radio} title="Can't reach the Shiplino daemon">
        Run <code className="font-mono">shiplino doctor</code> in a terminal.
      </Empty>
    );
  }

  const exportHref = `${API_BASE}/api/v1/export?format=csv&since=${days}d${project === "all" ? "" : `&project=${encodeURIComponent(project)}`}`;

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="font-semibold text-xl tracking-tight">Insights</h1>
          <p className="text-muted-foreground text-sm">
            What your agents did, what it cost, and how long they waited on you.
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Select value={project} onValueChange={setProject}>
            <SelectTrigger className="h-8 w-44" aria-label="Project">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">All projects</SelectItem>
              {projects.map((p) => (
                <SelectItem key={p.id} value={p.id}>
                  {p.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Select value={days} onValueChange={setDays}>
            <SelectTrigger className="h-8 w-36" aria-label="Time range">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {ranges.map(([v, label]) => (
                <SelectItem key={v} value={v}>
                  {label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Button variant="outline" size="sm" className="h-8" asChild>
            <a href={exportHref} download>
              <Download className="size-3.5" /> CSV
            </a>
          </Button>
        </div>
      </div>

      {!data ? (
        <div className="flex flex-col gap-4">
          <Skeleton className="h-28 w-full" />
          <Skeleton className="h-80 w-full" />
        </div>
      ) : data.totals.sessions === 0 && data.previous.sessions === 0 ? (
        <Empty icon={ChartColumn} title="Nothing to show for this period yet">
          Start any agent. Its work, time and cost show up here.
        </Empty>
      ) : (
        <>
          <Kpis t={data.totals} p={data.previous} days={data.days} />
          <div className="grid grid-cols-1 items-stretch gap-4 xl:grid-cols-12">
            <div className="xl:col-span-8">
              <DailyChart data={data} />
            </div>
            <div className="flex flex-col gap-4 xl:col-span-4">
              <ByAgent rows={data.agents} />
              <CostSources sources={data.cost_sources} />
            </div>
          </div>
          <div className="grid grid-cols-1 items-stretch gap-4 xl:grid-cols-12">
            <div className="xl:col-span-7">
              <ByProject rows={data.projects} />
            </div>
            <div className="xl:col-span-5">
              <Tools data={data} />
            </div>
          </div>
          <Models rows={data.models} />
        </>
      )}
    </div>
  );
}
