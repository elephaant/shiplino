"use client";

import { create } from "zustand";

/**
 * Which waits you've looked at: a waiting card is "new" until you open
 * it. Keyed by card id and the time it started waiting, so the next wait
 * of the same session is new again. Kept in this browser only.
 */
const KEY = "shiplino.seen-waits";

function load(): Record<string, string> {
  try {
    return JSON.parse(localStorage.getItem(KEY) ?? "{}") as Record<string, string>;
  } catch {
    return {};
  }
}

interface SeenState {
  seen: Record<string, string>;
  markSeen: (id: string, since?: string) => void;
}

export const useSeen = create<SeenState>((set) => ({
  seen: typeof window === "undefined" ? {} : load(),
  markSeen: (id, since) =>
    set((s) => {
      if (!since || s.seen[id] === since) return s;
      // Keep the newest 200 entries: old waits don't matter.
      const seen = Object.fromEntries([...Object.entries({ ...s.seen, [id]: since })].slice(-200));
      try {
        localStorage.setItem(KEY, JSON.stringify(seen));
      } catch {
        // Storage off (private window): it still works until reload.
      }
      return { seen };
    }),
}));

export function isUnseen(seen: Record<string, string>, id: string, since?: string): boolean {
  return !!since && seen[id] !== since;
}
