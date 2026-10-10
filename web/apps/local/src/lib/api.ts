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
  state?: "open" | "draft" | "merged" | "closed";
  checks?: "success" | "failure" | "pending";
  review?: "approved" | "changes_requested";
  title?: string;
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
  /** Least certain source of the line counts: "agent" | "computed" | "estimated". */
  lines_source?: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  cost_usd: number;
  tree_cost_usd: number;
  telemetry?: { requests: number; cost_usd: number };
  reported_cost_usd?: number;
  best_cost_usd: number;
  cost_source?: "reported" | "computed";
  /** "none": settled after doing work without recording any token usage. */
  usage?: "tokens" | "none";
  links?: Link[];
  /** The agent's own todo list: counts, and items (text only at capture level standard and full). */
  plan_total?: number;
  plan_done?: number;
  plan_items?: PlanItem[];
  waiting_ms: number;
  waiting_since?: string;
  waiting_reason?: WaitingReason;
  /** Set once the agent's hooks reported this session. */
  hook_seen?: boolean;
  /** Collector of the first activity: "hook" | "transcript" | "otlp" | "http" | "wrap". */
  activity_source?: string;
  /** How the project was found: "remote" | "git" | "dir" | "unsorted" (detail view only). */
  project_kind?: string;
}

export interface PlanItem {
  id?: string;
  text?: string;
  status?: "pending" | "in_progress" | "completed" | "cancelled" | "blocked";
}

/** Why a session waits on you, as the agent reported it. */
export type WaitingReason = "permission" | "question" | "idle";

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
  reason?: WaitingReason;
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

export interface InsightTotals {
  sessions: number;
  cost_usd: number;
  active_ms: number;
  waiting_ms: number;
  turns: number;
  tool_calls: number;
  tool_errors: number;
  files: number;
  lines_added: number;
  lines_removed: number;
  commits: number;
  prs: number;
  /** PRs whose GitHub state is merged (0 without the GitHub integration). */
  prs_merged: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

export interface InsightRow {
  key: string;
  name?: string;
  sessions: number;
  cost_usd: number;
  active_ms: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  lines_added: number;
  lines_removed: number;
}

export interface Insights {
  from: string;
  to: string;
  days: number;
  totals: InsightTotals;
  previous: InsightTotals;
  daily: { date: string; sessions: number; cost_usd: Record<string, number>; active_ms: number }[];
  agents: InsightRow[];
  projects: InsightRow[];
  models: InsightRow[];
  tools: { tool: string; kind: string; calls: number }[];
  cost_sources: Record<string, number>;
  failures: Failures;
}

/** The sessions behind a failure row, most recent first (at most 20 ids). */
export interface FailureLinks {
  session_count: number;
  sessions: string[];
}

export interface FailureScope extends FailureLinks {
  key: string;
  name?: string;
  tool_failures: number;
  shell_failures: number;
  denials: number;
  retry_loops: number;
  ended_badly: number;
}

/** Failure analytics. Built from metadata only: no command or error text. */
export interface Failures {
  tool_failures: number;
  shell_failures: number;
  denials: number;
  retry_loops: number;
  ended_badly: number;
  tools: (FailureLinks & {
    agent: string;
    tool: string;
    tool_raw: string;
    failures: number;
    denials: number;
    projects: string[];
  })[];
  shell: (FailureLinks & {
    program: string;
    exit_code: number;
    failures: number;
    agents: string[];
    projects: string[];
  })[];
  loops: {
    session: string;
    agent: string;
    project: string;
    tool: string;
    tool_raw: string;
    target: string;
    failures: number;
    first: string;
    last: string;
  }[];
  endings: { session: string; agent: string; project: string; status: "error" | "interrupted"; at: string }[];
  agents: FailureScope[];
  projects: FailureScope[];
}

export interface AgentStatus {
  /** Names this entry in /api/v1/agents/{key}/… (an agent can have two config files). */
  key: string;
  id: string;
  name: string;
  found: boolean;
  version?: string;
  hooks_path?: string;
  connected: boolean;
  current: boolean;
  problem?: string;
}

/** What connecting or removing an agent would write: a unified diff per file. */
export interface AgentPlan {
  key: string;
  name: string;
  action: "connect" | "remove";
  changes: { path: string; diff: string }[];
  problem?: string;
  note?: string;
}

export interface Settings {
  version: string;
  home: string;
  config_path: string;
  port: number;
  data_bytes: number;
  capture_level: string;
  extra_redaction_patterns: number;
  paused: boolean;
  paused_until?: string;
  notify: {
    enabled: boolean;
    waiting: boolean;
    finished: boolean;
    failed: boolean;
    min_turn_ms: number;
    /** Plan usage window alert threshold; 0 when off. */
    limit_percent: number;
    available: boolean;
    via?: string;
  };
  agents: AgentStatus[];
  health: {
    lines: number;
    events: number;
    unknown: number;
    bad: number;
    spool_backlog_bytes: number;
    transcripts: number;
    paused: boolean;
    watch_error?: string;
  };
  sync?: SyncState;
  budget: { spends: { scope: string; label: string; spent_usd: number; limit_usd: number }[]; digest?: string };
  update: UpdateState;
}

/** Update checks ([update] in config.toml), from the last check. */
export interface UpdateState {
  check: boolean;
  auto_install: boolean;
  channel?: string;
  /** A development build, never updated from releases. */
  dev: boolean;
  /** A newer release than the running version, if the last check found one. */
  available?: string;
  url?: string;
  checked_at?: string;
  error?: string;
  install_error?: string;
}

/** Cloud sync state. It never contains tokens. */
export interface SyncState {
  enabled: boolean;
  endpoint: string;
  signed_in: boolean;
  account?: string;
  role?: string;
  workspace_id?: string;
  workspace_name?: string;
  credential_store?: "keychain" | "file";
  credential_note?: string;
  send_titles: boolean;
  ignored?: string[];
  projects: string[];
  exclude: string[];
  last_upload?: string;
  uploaded: number;
  rejected: number;
  backlog: number;
  last_error?: string;
  last_error_at?: string;
  next_retry?: string;
  needs_login?: boolean;
  forbidden?: boolean;
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
  waiting_reason?: WaitingReason;
  waiting_since?: string;
  started_at: string;
  last_event_at: string;
  duration_ms: number;
  waiting_ms?: number;
  cost_usd: number;
  cost_source?: "reported" | "computed";
  usage?: "tokens" | "none";
  files: number;
  lines_added: number;
  lines_removed: number;
  lines_source?: string;
  links?: Link[];
  subagents?: SubagentRow[];
  plan_total?: number;
  plan_done?: number;
  tool_calls: number;
  active_ms: number;
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

export type SegmentState = "running" | "waiting" | "idle";

export interface Segment {
  start: string;
  end: string;
  state: SegmentState;
}

export interface TimelineRow {
  id: string;
  parent_id?: string;
  root_id: string;
  depth: number;
  agent: string;
  actor_type?: string;
  title?: string;
  status: Status;
  project_id?: string;
  started_at: string;
  ended_at?: string;
  cost_usd: number;
  segments: Segment[];
}

export interface Timeline {
  from: string;
  to: string;
  now: string;
  rows: TimelineRow[];
}

export interface FileSummary {
  path: string;
  op: string;
  edits: number;
  lines_added: number;
  lines_removed: number;
  /** Edits that have a stored diff. */
  patches: number;
  omitted?: string;
  actors: string[];
  last_at: string;
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

/** One conversation entry, read on demand from the agent's own transcript (never stored). */
export interface ConversationMessage {
  role: "user" | "assistant" | "tool";
  text?: string;
  /** The agent's own tool name (tool messages). */
  tool?: string;
  ts?: string;
  subagent?: string;
  todos?: { text: string; status?: string }[];
}

export interface Conversation {
  session_id: string;
  /** "transcript": the agent's file on this machine; "stored": Shiplino's own record; "none": not shown. */
  source: "transcript" | "stored" | "none";
  reason?: "capture_minimal" | "unsupported_agent" | "no_transcript" | "outside_roots" | "transcript_missing";
  note?: string;
  capture_level: "minimal" | "standard" | "full";
  total: number;
  offset: number;
  next_offset?: number;
  truncated?: boolean;
  messages: ConversationMessage[];
}

/** download fetches a file from the API and saves it in the browser. */
export async function download(path: string, fallbackName: string): Promise<void> {
  const res = await fetch(API_BASE + path, { credentials: "include" });
  if (!res.ok) throw new ApiError(res.status, res.statusText);
  const name = /filename="([^"]+)"/.exec(res.headers.get("Content-Disposition") ?? "")?.[1] ?? fallbackName;
  const url = URL.createObjectURL(await res.blob());
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 10_000); // some browsers read it after click returns
}
