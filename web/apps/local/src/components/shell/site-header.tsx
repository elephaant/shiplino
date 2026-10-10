"use client";

import { Gauge, Monitor, Moon, Sun } from "lucide-react";
import Link from "next/link";
import { useTheme } from "next-themes";
import { useEffect, useMemo, useState } from "react";
import { AgentDot } from "@/components/common/agent-dot";
import { limitTone } from "@/components/insights/plan-limits";
import { CommandMenu } from "@/components/shell/command-menu";
import { Button } from "@/components/ui/button";
import { Separator } from "@/components/ui/separator";
import { SidebarTrigger } from "@/components/ui/sidebar";
import { agentName, formatTokens } from "@/lib/format";
import { describeWindow, formatReset, type LimitWindow, useLimits, windowValue } from "@/lib/limits";
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

/** The window closest to its limit per agent: "Codex 62% · resets 14:20". */
function LimitStrip() {
  const limits = useLimits();
  const shown = useMemo(() => {
    const best = new Map<string, LimitWindow>();
    for (const w of limits?.windows ?? []) {
      const pct = (x: LimitWindow) => (x.reached ? 101 : (x.used_percent ?? -1));
      const cur = best.get(w.agent);
      // Reported percentages first, else the current 5-hour estimate.
      if (!cur || pct(w) > pct(cur) || (pct(cur) < 0 && pct(w) < 0 && w.window_minutes < cur.window_minutes))
        best.set(w.agent, w);
    }
    return [...best.values()];
  }, [limits]);
  if (shown.length === 0) return null;
  return (
    <Link
      href="/insights/#limits"
      className="hidden shrink-0 items-center gap-3 whitespace-nowrap rounded-md px-2 py-1 text-sm hover:bg-accent md:flex"
      aria-label={`Plan limits: ${shown.map(describeWindow).join("; ")}`}
      title={(limits?.windows ?? []).map(describeWindow).join("\n")}
    >
      <Gauge className="size-3.5 text-muted-foreground" aria-hidden />
      {shown.map((w) => (
        <span key={w.agent} className="flex items-center gap-1.5">
          <AgentDot agent={w.agent} className="size-2" />
          <span className="hidden lg:inline">{agentName(w.agent)}</span>
          <span className={`font-mono tabular-nums ${limitTone(w)}`}>
            {w.source === "estimate" ? `${w.window} ~${formatTokens(w.tokens ?? 0)}` : windowValue(w)}
          </span>
          {w.resets_at && (
            <span className="font-mono text-muted-foreground text-xs tabular-nums">
              · resets {formatReset(w.resets_at)}
            </span>
          )}
        </span>
      ))}
    </Link>
  );
}

function ThemeToggle() {
  const { theme: chosen, setTheme } = useTheme();
  // The prerendered page can't know the stored choice: render "system"
  // until mounted so hydration matches, then the real choice.
  const [mounted, setMounted] = useState(false);
  useEffect(() => setMounted(true), []);
  const theme = mounted ? chosen : undefined;
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
      <LimitStrip />
      <div className="ml-auto flex items-center gap-2">
        <CommandMenu />
        <ThemeToggle />
      </div>
    </header>
  );
}
