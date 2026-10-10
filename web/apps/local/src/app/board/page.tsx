"use client";

import { Kanban, Radio } from "lucide-react";
import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useCallback, useEffect, useMemo, useState } from "react";
import { Board } from "@/components/board/board";
import { CardSheet } from "@/components/board/card-sheet";
import { NewCard } from "@/components/board/new-card";
import { Empty } from "@/components/common/empty";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { api, type BoardCard, type BoardResponse, type ProjectSummary } from "@/lib/api";
import { agentName, formatCost } from "@/lib/format";
import { useLive } from "@/lib/live";

function ProjectPicker() {
  const router = useRouter();
  const [projects, setProjects] = useState<ProjectSummary[] | null>(null);
  useEffect(() => {
    api<{ projects: ProjectSummary[] }>("/api/v1/projects")
      .then((r) => {
        setProjects(r.projects);
        if (r.projects.length === 1 && r.projects[0])
          router.replace(`/board/?project=${encodeURIComponent(r.projects[0].id)}`);
      })
      .catch(() => setProjects([]));
  }, [router]);
  if (!projects) return <Skeleton className="h-40 w-full" />;
  if (projects.length === 0) return <Empty icon={Radio} title="Start any agent. It'll appear here within a second." />;
  return (
    <Empty icon={Kanban} title="Pick a project">
      Choose a project in the sidebar to see its board.
    </Empty>
  );
}

function BoardPage() {
  const params = useSearchParams();
  const router = useRouter();
  const project = params.get("project");
  const sprint = params.get("sprint") ?? "current";
  const version = useLive((s) => s.version);
  const [data, setData] = useState<BoardResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  const [agent, setAgent] = useState("all");
  const [open, setOpen] = useState<BoardCard | null>(null);

  const load = useCallback(() => {
    if (!project) return;
    api<BoardResponse>(`/api/v1/projects/${encodeURIComponent(project)}/board?sprint=${encodeURIComponent(sprint)}`)
      .then((d) => {
        setData(d);
        setError(null);
      })
      .catch((e: Error) => setError(e.message));
  }, [project, sprint]);

  // Refetch on live changes, at most every 400 ms.
  // biome-ignore lint/correctness/useExhaustiveDependencies: version triggers a refetch
  useEffect(() => {
    const t = setTimeout(load, data ? 400 : 0);
    return () => clearTimeout(t);
  }, [load, version]);

  const columns = useMemo(() => {
    if (!data) return [];
    const q = query.trim().toLowerCase();
    return data.columns.map((c) => ({
      ...c,
      cards: c.cards.filter(
        (card) =>
          (agent === "all" || card.agent === agent) &&
          (!q || card.title.toLowerCase().includes(q) || card.branch?.toLowerCase().includes(q)),
      ),
    }));
  }, [data, query, agent]);

  const agents = useMemo(() => {
    const set = new Set<string>();
    for (const c of data?.columns ?? []) for (const card of c.cards) if (card.agent) set.add(card.agent);
    return [...set].sort();
  }, [data]);

  if (!project) return <ProjectPicker />;
  if (error && !data)
    return (
      <Empty icon={Radio} title="Couldn't load this board">
        {error}
      </Empty>
    );
  if (!data) return <Skeleton className="h-96 w-full" />;

  const total = data.columns.reduce((n, c) => n + c.cards.reduce((m, card) => m + card.cost_usd, 0), 0);
  const sprints = Array.from({ length: data.current_sprint }, (_, i) => data.current_sprint - i);
  const setSprint = (v: string) => router.replace(`/board/?project=${encodeURIComponent(project)}&sprint=${v}`);

  return (
    <div className="flex min-w-0 flex-col gap-4">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold tracking-tight">{data.project.name}</h1>
          <p className="text-sm text-muted-foreground">
            {data.sprint ? data.sprint.name : "All sprints"} · <span className="font-mono">{formatCost(total)}</span>
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Input
            placeholder="Filter cards"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            className="h-8 w-44"
            aria-label="Filter cards"
          />
          {agents.length > 1 && (
            <Select value={agent} onValueChange={setAgent}>
              <SelectTrigger size="sm" className="w-36" aria-label="Agent">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">All agents</SelectItem>
                {agents.map((a) => (
                  <SelectItem key={a} value={a}>
                    {agentName(a)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
          <Select value={sprint === "current" ? String(data.current_sprint) : sprint} onValueChange={setSprint}>
            <SelectTrigger size="sm" className="w-44" aria-label="Sprint">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {sprints.map((n) => (
                <SelectItem key={n} value={String(n)}>
                  Sprint {n}
                  {n === data.current_sprint ? " (current)" : ""}
                </SelectItem>
              ))}
              <SelectItem value="all">All sprints</SelectItem>
            </SelectContent>
          </Select>
          <NewCard projectId={project} onCreated={load} />
        </div>
      </div>
      <Board columns={columns} onChanged={load} onOpen={setOpen} />
      <CardSheet card={open} onClose={() => setOpen(null)} onChanged={load} />
    </div>
  );
}

export default function Page() {
  return (
    <Suspense>
      <BoardPage />
    </Suspense>
  );
}
