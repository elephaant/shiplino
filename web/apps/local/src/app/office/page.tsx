"use client";

import { Bell, Brain, Building2, FileText, Moon, PartyPopper, Siren, SquarePen, Terminal } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { Empty } from "@/components/common/empty";
import { OfficeFloor } from "@/components/office/office-floor";
import { api, type ProjectSummary } from "@/lib/api";
import { useLive } from "@/lib/live";
import { present } from "@/lib/office";

// Poses depend on time (celebrating, then leaving; idle, then asleep).
const CLOCK_MS = 15_000;

const legend = [
  { icon: SquarePen, label: "Typing" },
  { icon: FileText, label: "Reading" },
  { icon: Terminal, label: "At the terminal" },
  { icon: Brain, label: "Thinking" },
  { icon: Bell, label: "At the bell: needs you" },
  { icon: Moon, label: "Asleep" },
  { icon: PartyPopper, label: "Done" },
  { icon: Siren, label: "Failed" },
];

export default function OfficePage() {
  const all = useLive((s) => s.sessions);
  const [now, setNow] = useState(() => Date.now());
  const [names, setNames] = useState<Record<string, string>>({});

  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), CLOCK_MS);
    return () => clearInterval(t);
  }, []);

  const sessions = useMemo(() => Object.values(all).filter((s) => present(s, now)), [all, now]);
  const projects = [...new Set(sessions.map((s) => s.project_id ?? ""))].sort().join("\n");
  // biome-ignore lint/correctness/useExhaustiveDependencies: refetch names when a new project appears
  useEffect(() => {
    api<{ projects: ProjectSummary[] }>("/api/v1/projects")
      .then((r) => setNames(Object.fromEntries(r.projects.map((p) => [p.id, p.name]))))
      .catch(() => {});
  }, [projects]);

  return (
    <div className="flex flex-col gap-4">
      <div>
        <h1 className="font-semibold text-xl tracking-tight">Office</h1>
        <p className="text-muted-foreground text-sm">
          Every live session and subagent at a desk, one room per project. Hover a character for details, click to open
          its session.
        </p>
      </div>
      {sessions.length === 0 ? (
        <Empty icon={Building2} title="Nobody's in the office">
          Start any agent. It&apos;ll appear here within a second.
        </Empty>
      ) : (
        <OfficeFloor sessions={sessions} names={names} now={now} />
      )}
      <ul className="flex flex-wrap gap-x-4 gap-y-1 text-muted-foreground text-xs">
        {legend.map((l) => (
          <li key={l.label} className="flex items-center gap-1">
            <l.icon className="size-3.5" aria-hidden />
            {l.label}
          </li>
        ))}
        <li>Subagents wear a cap in their agent&apos;s color.</li>
      </ul>
    </div>
  );
}
