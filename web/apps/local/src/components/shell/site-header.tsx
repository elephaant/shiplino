// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

"use client";

import { Monitor, Moon, Sun } from "lucide-react";
import Link from "next/link";
import { useTheme } from "next-themes";
import { useEffect, useMemo } from "react";
import { CommandMenu } from "@/components/shell/command-menu";
import { Button } from "@/components/ui/button";
import { Separator } from "@/components/ui/separator";
import { SidebarTrigger } from "@/components/ui/sidebar";
import { useLive } from "@/lib/live";

function LiveStrip() {
  const sessions = useLive((s) => s.sessions);
  const { running, waiting } = useMemo(() => {
    let running = 0;
    let waiting = 0;
    // Count sessions like the board does; subagents show on their cards.
    for (const s of Object.values(sessions)) {
      if (s.parent_id) continue;
      if (s.status === "running") running++;
      else if (s.status === "waiting") waiting++;
    }
    return { running, waiting };
  }, [sessions]);

  useEffect(() => {
    document.title = waiting > 0 ? `(${waiting}) Shiplino` : "Shiplino";
  }, [waiting]);

  return (
    <Link
      href="/#needs-you"
      aria-label={`${running} running, ${waiting} waiting on you`}
      className="flex shrink-0 items-center gap-3 whitespace-nowrap rounded-md px-2 py-1 text-sm hover:bg-accent"
    >
      <span className="flex items-center gap-1.5">
        <span className="size-2 rounded-full bg-status-running" aria-hidden />
        <span className="font-mono tabular-nums">{running}</span>
        <span className="hidden sm:inline">running</span>
      </span>
      <span className="flex items-center gap-1.5">
        <span className={`size-2 rounded-full bg-status-waiting ${waiting ? "animate-pulse" : ""}`} aria-hidden />
        <span className="font-mono tabular-nums">{waiting}</span>
        <span className="hidden sm:inline">waiting on you</span>
      </span>
    </Link>
  );
}

function ThemeToggle() {
  const { theme, setTheme } = useTheme();
  const next = theme === "light" ? "dark" : theme === "dark" ? "system" : "light";
  const Icon = theme === "light" ? Sun : theme === "dark" ? Moon : Monitor;
  return (
    <Button
      variant="ghost"
      size="icon"
      onClick={() => setTheme(next)}
      aria-label={`Theme: ${theme ?? "system"}. Switch to ${next}`}
    >
      <Icon className="size-4" />
    </Button>
  );
}

export function SiteHeader() {
  return (
    <header className="flex h-12 shrink-0 items-center gap-2 border-b px-4">
      <SidebarTrigger className="-ml-1" />
      <Separator orientation="vertical" className="mr-1 h-4" />
      <LiveStrip />
      <div className="ml-auto flex items-center gap-2">
        <CommandMenu />
        <ThemeToggle />
      </div>
    </header>
  );
}
