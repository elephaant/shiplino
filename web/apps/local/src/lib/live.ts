// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

"use client";

import { create } from "zustand";
import { api, liveURL, type Session } from "./api";

interface LiveState {
  connected: boolean;
  sessions: Record<string, Session>;
  /** Bumped whenever something on a board may have changed; pages refetch on change. */
  version: number;
  setSessions: (list: Session[]) => void;
  upsert: (s: Session) => void;
  bump: () => void;
  setConnected: (c: boolean) => void;
}

export const useLive = create<LiveState>((set) => ({
  connected: false,
  sessions: {},
  version: 0,
  setSessions: (list) => set({ sessions: Object.fromEntries(list.map((s) => [s.id, s])) }),
  upsert: (s) => set((st) => ({ sessions: { ...st.sessions, [s.id]: s }, version: st.version + 1 })),
  bump: () => set((st) => ({ version: st.version + 1 })),
  setConnected: (connected) => set({ connected }),
}));

let started = false;

/** Opens the live connection once per page load and keeps it open. */
export function startLive() {
  if (started || typeof window === "undefined") return;
  started = true;
  const { setSessions, upsert, bump, setConnected } = useLive.getState();

  const load = () =>
    api<{ sessions: Session[] }>("/api/v1/sessions?limit=500")
      .then((r) => setSessions(r.sessions))
      .catch(() => {});

  const connect = () => {
    const ws = new WebSocket(liveURL());
    ws.onopen = () => {
      setConnected(true);
      load();
      bump();
    };
    ws.onmessage = (m) => {
      const msg = JSON.parse(m.data as string) as { t: string; session?: Session };
      if (msg.t === "session.update" && msg.session) upsert(msg.session);
      else if (msg.t === "board.changed") bump();
    };
    ws.onclose = () => {
      setConnected(false);
      setTimeout(connect, 2000);
    };
  };
  connect();
}
