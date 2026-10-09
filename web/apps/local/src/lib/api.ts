// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

// Same origin in production (the daemon serves this app). In development
// set NEXT_PUBLIC_SHIPLINO_API=http://localhost:4777 and start the daemon
// with SHIPLINO_DEV_ORIGIN=http://localhost:3000.
export const API_BASE = process.env.NEXT_PUBLIC_SHIPLINO_API ?? "";

export type Status = "running" | "waiting" | "idle" | "review" | "done" | "failed";

export interface Link {
  kind: "pr" | "push" | "commit";
  message?: string;
  url?: string;
  number?: number;
  ref?: string;
  action?: string;
}

export interface Session {
  id: string;
  agent: string;
  agent_version?: string;
  parent_id?: string;
  root_id: string;
  actor_type?: string;
  cwd?: string;
  project_id?: string;
  branch?: string;
  title?: string;
  title_source?: string;
  model?: string;
  status: Status;
  now_doing?: string;
  started_at: string;
  ended_at?: string;
  last_event_at: string;
  turns: number;
  tool_calls: number;
  tool_errors: number;
  files?: string[];
  lines_added: number;
  lines_removed: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  cost_usd: number;
  tree_cost_usd: number;
  reported_cost_usd?: number;
  best_cost_usd: number;
  cost_source?: "reported" | "computed";
  links?: Link[];
  waiting_ms: number;
  waiting_since?: string;
}

export interface Sprint {
  number: number;
  name: string;
  starts: string;
  ends: string;
}

export interface ProjectSummary {
  id: string;
  name: string;
  kind: string;
  remote?: string;
  first_seen: string;
  last_seen: string;
  counts: Record<string, number>;
  sessions: number;
  cost_usd: number;
}

export interface OverviewRow extends ProjectSummary {
  sprint: Sprint;
  columns: Record<string, number>;
  running_subagents: number;
  sprint_cost_usd: number;
}

export interface NeedsYou {
  session_id: string;
  root_id: string;
  project_id: string;
  agent: string;
  title: string;
  message: string;
  since: string;
}

export interface SearchHit {
  event_id: string;
  session_id: string;
  kind: string;
  ts: string;
  snippet: string;
  session_title?: string;
  agent?: string;
  project_id?: string;
}

export interface Overview {
  projects: OverviewRow[];
  needs_you: NeedsYou[];
}

export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
  }
}

export async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(API_BASE + path, {
    ...init,
    credentials: "include",
    headers: { "Content-Type": "application/json", ...init?.headers },
  });
  if (!res.ok) {
    let msg = res.statusText;
    try {
      msg = ((await res.json()) as { error?: string }).error ?? msg;
    } catch {}
    throw new ApiError(res.status, msg);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

export function liveURL(): string {
  const base = API_BASE || (typeof window === "undefined" ? "" : window.location.origin);
  return `${base.replace(/^http/, "ws")}/api/v1/live`;
}

export type ColumnId = "backlog" | "running" | "waiting" | "review" | "done" | "failed";

export interface SubagentRow {
  id: string;
  type?: string;
  status: Status;
  now_doing?: string;
  cost_usd: number;
}

export interface BoardCard {
  id: string;
  origin: "auto" | "manual";
  title: string;
  title_source?: string;
  column: ColumnId;
  pinned: boolean;
  position?: number;
  project_id: string;
  agent?: string;
  model?: string;
  status?: Status;
  branch?: string;
  now_doing?: string;
  started_at: string;
  last_event_at: string;
  duration_ms: number;
  waiting_ms?: number;
  cost_usd: number;
  cost_source?: "reported" | "computed";
  files: number;
  lines_added: number;
  lines_removed: number;
  links?: Link[];
  subagents?: SubagentRow[];
  sprint: number;
  rolled_over_from?: number;
  notes?: string;
}

export interface BoardColumn {
  id: ColumnId;
  name: string;
  cards: BoardCard[];
}

export interface BoardResponse {
  project: ProjectSummary;
  columns: BoardColumn[];
  current_sprint: number;
  sprint?: Sprint;
}

export interface AgentEvent {
  id: string;
  ts: string;
  kind: string;
  agent: { name: string; version?: string };
  collector: string;
  session_id: string;
  actor_id?: string;
  actor_type?: string;
  data?: Record<string, unknown>;
}
