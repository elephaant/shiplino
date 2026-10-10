// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

"use client";

import { cn } from "cn";
import { ChartColumn, Kanban, LayoutDashboard, Settings } from "lucide-react";
import Link from "next/link";
import { usePathname, useSearchParams } from "next/navigation";
import { Suspense, useEffect, useState } from "react";
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuBadge,
  SidebarMenuButton,
  SidebarMenuItem,
} from "@/components/ui/sidebar";
import { api, type ProjectSummary } from "@/lib/api";
import { useLive } from "@/lib/live";

function ProjectList() {
  const pathname = usePathname();
  const params = useSearchParams();
  const version = useLive((s) => s.version);
  const sessions = useLive((s) => s.sessions);
  const [projects, setProjects] = useState<ProjectSummary[]>([]);

  // biome-ignore lint/correctness/useExhaustiveDependencies: refetch when live data changes
  useEffect(() => {
    api<{ projects: ProjectSummary[] }>("/api/v1/projects")
      .then((r) => setProjects(r.projects))
      .catch(() => {});
  }, [version]);

  const live = (id: string) => {
    let running = 0;
    let waiting = 0;
    for (const s of Object.values(sessions)) {
      if (s.project_id !== id || s.parent_id) continue;
      if (s.status === "running") running++;
      if (s.status === "waiting") waiting++;
    }
    return { running, waiting };
  };

  return (
    <SidebarGroup>
      <SidebarGroupLabel>Projects</SidebarGroupLabel>
      <SidebarMenu>
        {projects.length === 0 && <p className="px-2 py-1 text-xs text-muted-foreground">No projects yet</p>}
        {projects.map((p) => {
          const { running, waiting } = live(p.id);
          const active = pathname.startsWith("/board") && params.get("project") === p.id;
          return (
            <SidebarMenuItem key={p.id}>
              <SidebarMenuButton asChild isActive={active} tooltip={p.remote ?? p.name}>
                <Link href={`/board/?project=${encodeURIComponent(p.id)}`}>
                  <span
                    className={cn(
                      "size-2 shrink-0 rounded-full",
                      waiting ? "bg-status-waiting" : running ? "bg-status-running" : "bg-muted-foreground/30",
                    )}
                    aria-hidden
                  />
                  <span className="truncate">{p.name}</span>
                </Link>
              </SidebarMenuButton>
              {(running > 0 || waiting > 0) && (
                <SidebarMenuBadge className="font-mono tabular-nums">
                  {running}
                  {waiting > 0 && <span className="ml-1 text-status-waiting">·{waiting}</span>}
                </SidebarMenuBadge>
              )}
            </SidebarMenuItem>
          );
        })}
      </SidebarMenu>
    </SidebarGroup>
  );
}

const nav = [
  { href: "/", label: "Overview", icon: LayoutDashboard },
  { href: "/board/", label: "Board", icon: Kanban },
  { href: "/insights/", label: "Insights", icon: ChartColumn },
];

export function AppSidebar() {
  const pathname = usePathname();
  const connected = useLive((s) => s.connected);
  return (
    <Sidebar variant="inset" collapsible="icon">
      <SidebarHeader>
        <Link href="/" className="flex items-center gap-2 px-2 py-1.5">
          {/* biome-ignore lint/performance/noImgElement: static export, no image optimizer */}
          <img src="/logo.svg" alt="" width={28} height={28} className="size-7" />
          <span className="font-semibold tracking-tight group-data-[collapsible=icon]:hidden">shiplino</span>
        </Link>
      </SidebarHeader>
      <SidebarContent>
        <SidebarGroup>
          <SidebarMenu>
            {nav.map((n) => (
              <SidebarMenuItem key={n.href}>
                <SidebarMenuButton
                  asChild
                  isActive={n.href === "/" ? pathname === "/" : pathname.startsWith(n.href)}
                  tooltip={n.label}
                >
                  <Link href={n.href}>
                    <n.icon aria-hidden />
                    <span>{n.label}</span>
                  </Link>
                </SidebarMenuButton>
              </SidebarMenuItem>
            ))}
          </SidebarMenu>
        </SidebarGroup>
        <Suspense>
          <ProjectList />
        </Suspense>
      </SidebarContent>
      <SidebarFooter>
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton asChild isActive={pathname.startsWith("/settings")} tooltip="Settings">
              <Link href="/settings/">
                <Settings aria-hidden />
                <span>Settings</span>
              </Link>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
        <p className="flex items-center gap-2 px-2 text-xs text-muted-foreground group-data-[collapsible=icon]:hidden">
          <span
            className={cn("size-1.5 rounded-full", connected ? "bg-status-done" : "bg-status-failed")}
            aria-hidden
          />
          {connected ? "Daemon connected" : "Daemon offline"}
        </p>
      </SidebarFooter>
    </Sidebar>
  );
}
