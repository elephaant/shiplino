"use client";

import { useEffect } from "react";
import { create } from "zustand";
import { api } from "./api";
import { agentName, formatTokens } from "./format";

/** One plan usage window of one agent (GET /api/v1/limits). */
export interface LimitWindow {
  agent: string;
  /** "5h", "7d", … */
  window: string;
  window_minutes: number;
  limit_id?: string;
  /** Absent on estimates: plan quotas aren't published. */
  used_percent?: number;
  reached?: boolean;
  resets_at?: string;
  plan?: string;
  /** "reported": the agent's own numbers. "estimate": tokens Shiplino summed. */
  source: "reported" | "estimate";
  tokens?: number;
  cost_usd?: number;
  updated_at?: string;
}

export interface Limits {
  windows: LimitWindow[];
  /** "plan" or "api" per agent, as configured or detected. */
  plans: Record<string, "plan" | "api">;
}

const useLimitsStore = create<{ data: Limits | null; set: (d: Limits) => void }>((set) => ({
  data: null,
  set: (data) => set({ data }),
}));

let users = 0;
let timer: ReturnType<typeof setInterval> | undefined;

function load() {
  api<Limits>("/api/v1/limits")
    .then((d) => useLimitsStore.getState().set(d))
    .catch(() => {});
}

/** Plan usage windows, refreshed every 30 seconds while any component uses them. */
export function useLimits(): Limits | null {
  useEffect(() => {
    if (users++ === 0) {
      load();
      timer = setInterval(load, 30_000);
    }
    return () => {
      if (--users === 0) clearInterval(timer);
    };
  }, []);
  return useLimitsStore((s) => s.data);
}

/** Whether an agent runs on a flat-rate plan, so its $ figures are API-equivalent. */
export function onPlan(limits: Limits | null, agent: string | undefined): boolean {
  return !!agent && limits?.plans[agent] === "plan";
}

export const API_EQUIVALENT =
  "API-equivalent: what this would cost at API list prices. On a flat-rate plan you aren't billed per token.";

export function windowLabel(w: Pick<LimitWindow, "window" | "limit_id">): string {
  const name = { "5h": "5-hour", "7d": "weekly", "1d": "daily" }[w.window] ?? w.window;
  return w.limit_id ? `${name} (${w.limit_id})` : name;
}

/** "14:20" today, else "Mon 14:20". */
export function formatReset(iso: string | undefined, now = new Date()): string {
  if (!iso) return "";
  const d = new Date(iso);
  const time = d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  return d.toDateString() === now.toDateString() ? time : `${d.toLocaleDateString([], { weekday: "short" })} ${time}`;
}

/** Short state: "62%", "limit reached", "1.2M tokens (est.)". */
export function windowValue(w: LimitWindow): string {
  if (w.reached) return "limit reached";
  if (w.used_percent !== undefined) return `${Math.round(w.used_percent)}%`;
  return `${formatTokens(w.tokens ?? 0)} tokens (est.)`;
}

/** One line for tooltips and screen readers. */
export function describeWindow(w: LimitWindow): string {
  const reset = w.resets_at ? ` · resets ${formatReset(w.resets_at)}` : "";
  const src =
    w.source === "reported"
      ? "reported by the agent"
      : w.window === "7d"
        ? "estimate: tokens in the last 7 days"
        : "estimate: tokens in the current window";
  return `${agentName(w.agent)} ${windowLabel(w)}: ${windowValue(w)}${reset} (${src})`;
}
