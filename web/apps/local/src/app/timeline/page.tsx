// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

"use client";

import { ChartGantt, ChevronLeft, ChevronRight, Radio } from "lucide-react";
import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useEffect, useRef, useState } from "react";
import { Empty } from "@/components/common/empty";
import { Gantt, Legend, type Zoom, zooms } from "@/components/timeline/gantt";
import { Button } from "@/components/ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { api, type ProjectSummary, type Timeline } from "@/lib/api";
import { useLive } from "@/lib/live";

// Live changes arrive many times a second while agents work; the chart
// refreshes at most this often. The clock also moves the "now" line.
const REFRESH_MS = 2000;
const CLOCK_MS = 15_000;

function range(zoom: Zoom, end: number | null, now: number) {
  const { span } = zooms[zoom];
  // Following now: leave a little room on the right of the "now" line.
  const to = end ?? now + span * 0.04;
  return { from: to - span, to };
}

function TimelinePage() {
  const params = useSearchParams();
  const router = useRouter();
  const project = params.get("project") ?? "all";
  const zoom = (params.get("zoom") as Zoom) in zooms ? (params.get("zoom") as Zoom) : "day";
  const version = useLive((s) => s.version);
  const [projects, setProjects] = useState<ProjectSummary[]>([]);
  const [end, setEnd] = useState<number | null>(null); // null: follow now
  const [now, setNow] = useState(() => Date.now());
  const [data, setData] = useState<Timeline | null>(null);
  const [error, setError] = useState<string | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const setParam = (key: string, value: string, fallback: string) => {
    const q = new URLSearchParams(params.toString());
    if (value === fallback) q.delete(key);
    else q.set(key, value);
    const s = q.toString();
    router.replace(`/timeline/${s ? `?${s}` : ""}`);
  };

  useEffect(() => {
    api<{ projects: ProjectSummary[] }>("/api/v1/projects")
      .then((r) => setProjects(r.projects))
      .catch(() => {});
  }, []);

  // Throttle: the first change schedules one refresh, later ones join it.
  // biome-ignore lint/correctness/useExhaustiveDependencies: refresh on live changes
  useEffect(() => {
    if (timer.current) return;
    timer.current = setTimeout(() => {
      timer.current = null;
      setNow(Date.now());
    }, REFRESH_MS);
  }, [version]);
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), CLOCK_MS);
    return () => {
      clearInterval(t);
      if (timer.current) clearTimeout(timer.current);
    };
  }, []);

  const { from, to } = range(zoom, end, now);
  useEffect(() => {
    const q = new URLSearchParams({ from: new Date(from).toISOString(), to: new Date(to).toISOString() });
    if (project !== "all") q.set("project", project);
    api<Timeline>(`/api/v1/timeline?${q}`)
      .then((d) => {
        setData(d);
        setError(null);
      })
      .catch((e: Error) => setError(e.message));
  }, [from, to, project]);

  const pan = (dir: -1 | 1) => {
    const next = (end ?? now) + (dir * zooms[zoom].span) / 2;
    setEnd(next >= Date.now() ? null : next);
  };

  if (error && !data) {
    return (
      <Empty icon={Radio} title="Can't reach the Shiplino daemon">
        Run <code className="font-mono">shiplino doctor</code> in a terminal.
      </Empty>
    );
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="font-semibold text-xl tracking-tight">Timeline</h1>
          <p className="text-muted-foreground text-sm">When each agent worked, waited on you, or sat idle.</p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Select value={project} onValueChange={(v) => setParam("project", v, "all")}>
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
          <fieldset className="flex rounded-md border p-0.5" aria-label="Zoom">
            {(Object.keys(zooms) as Zoom[]).map((z) => (
              <Button
                key={z}
                size="sm"
                variant={z === zoom ? "secondary" : "ghost"}
                className="h-7 px-2.5"
                aria-pressed={z === zoom}
                onClick={() => setParam("zoom", z, "day")}
              >
                {zooms[z].label}
              </Button>
            ))}
          </fieldset>
          <div className="flex items-center gap-1">
            <Button size="icon" variant="outline" className="size-8" aria-label="Earlier" onClick={() => pan(-1)}>
              <ChevronLeft className="size-4" />
            </Button>
            <Button
              size="sm"
              variant={end === null ? "secondary" : "outline"}
              className="h-8"
              aria-pressed={end === null}
              onClick={() => setEnd(null)}
            >
              Now
            </Button>
            <Button
              size="icon"
              variant="outline"
              className="size-8"
              aria-label="Later"
              disabled={end === null}
              onClick={() => pan(1)}
            >
              <ChevronRight className="size-4" />
            </Button>
          </div>
        </div>
      </div>

      <div className="flex flex-wrap items-center justify-between gap-2">
        <Legend />
        <p className="font-mono text-muted-foreground text-xs tabular-nums">
          {new Date(from).toLocaleString([], { dateStyle: "medium", timeStyle: "short" })} –{" "}
          {end === null ? "now" : new Date(to).toLocaleString([], { dateStyle: "medium", timeStyle: "short" })}
        </p>
      </div>

      {!data ? (
        <Skeleton className="h-80 w-full" />
      ) : data.rows.length === 0 ? (
        <Empty icon={ChartGantt} title="No sessions in this window">
          Start any agent. It&apos;ll appear here within a second. Or zoom out to see earlier work.
        </Empty>
      ) : (
        <Gantt data={data} zoom={zoom} now={now} />
      )}
      <p className="text-muted-foreground text-xs">
        Hover a bar for details, click a row to open the session. Keys: <kbd className="font-mono">j</kbd>/
        <kbd className="font-mono">k</kbd> to move, <kbd className="font-mono">Enter</kbd> to open.
      </p>
    </div>
  );
}

export default function Page() {
  return (
    <Suspense>
      <TimelinePage />
    </Suspense>
  );
}
