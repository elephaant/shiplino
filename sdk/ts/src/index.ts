// Shiplino SDK: report sessions, turns, tool calls and usage from a custom
// agent to the local Shiplino daemon (POST /api/v1/ingest).
//
// It never throws into the host agent. Events are queued and sent in the
// background in batches; when the daemon isn't installed the client is a
// no-op, and when it isn't reachable events wait in a bounded queue.

import { randomBytes, randomUUID } from "node:crypto";
import { readFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

export interface ShiplinoOptions {
  /** Your agent's name: up to 64 characters, without ":", "/" or spaces. */
  agent: string;
  agentVersion?: string;
  /** Daemon address. Default: http://127.0.0.1:<port from the Shiplino home>. */
  url?: string;
  /** API token. Default: the `token` file in the Shiplino home. */
  token?: string;
  /** Shiplino home. Default: $SHIPLINO_HOME, else ~/.shiplino. */
  home?: string;
  /** Send queued events this often (ms). Default 1000. */
  flushIntervalMs?: number;
  /** Events per request; reaching it also triggers a send. Default 100. */
  batchSize?: number;
  /** Events kept while the daemon is unreachable; the oldest go first. Default 10000. */
  maxQueue?: number;
  /** Request timeout (ms). Default 5000. */
  timeoutMs?: number;
  /** Where warnings go (each kind once per client). Default: console.warn. */
  onWarning?: (message: string) => void;
}

/** Counters for what the client did, for health checks and debugging. */
export interface Stats {
  /** False when the daemon isn't installed or the options are invalid. */
  enabled: boolean;
  queued: number;
  sent: number;
  duplicates: number;
  /** Dropped because the queue was full (daemon unreachable). */
  dropped: number;
  /** Refused by the daemon (invalid events, bad token, recording paused). */
  rejected: number;
  lastError?: string;
}

export interface SessionOptions {
  title?: string;
  /** Working directory, used to put the session in a project. Default: process.cwd(). */
  cwd?: string;
  model?: string;
  /** Your own session id (unique per agent). Default: a random UUID. */
  id?: string;
}

export interface UsageOptions {
  model: string;
  inputTokens?: number;
  outputTokens?: number;
  cacheRead?: number;
  cacheWrite?: number;
  /** The cost your agent computed. Without it, the daemon prices the tokens itself. */
  costUsd?: number;
  /** The provider's response id, if you have one. */
  messageId?: string;
}

export type EndStatus = "ok" | "error";

type Data = Record<string, unknown>;

interface WireEvent {
  id: string;
  v: 1;
  ts: string;
  kind: string;
  agent: { name: string; version?: string };
  session_id: string;
  actor_id?: string;
  parent_actor?: string;
  actor_type?: string;
  project?: { cwd: string };
  data?: Data;
}

const toolKinds: Record<string, string> = {
  edit: "edit",
  multiedit: "edit",
  write: "write",
  read: "read",
  bash: "shell",
  shell: "shell",
  exec: "shell",
  grep: "search",
  glob: "search",
  search: "search",
  ls: "search",
  fetch: "web",
  webfetch: "web",
  websearch: "web",
  web: "web",
  task: "task",
  agent: "task",
};

/** The universal tool kind for a tool name ("other" when unknown). */
export function toolKind(name: string): string {
  const n = name.toLowerCase();
  if (n.startsWith("mcp__") || n.startsWith("mcp.")) return "mcp";
  return toolKinds[n] ?? "other";
}

function summarize(input: unknown): string | undefined {
  let s: string | undefined;
  if (typeof input === "string") {
    s = input;
  } else if (input && typeof input === "object") {
    const o = input as Record<string, unknown>;
    for (const k of ["path", "file_path", "command", "cmd", "query", "pattern", "url"]) {
      if (typeof o[k] === "string") {
        s = o[k] as string;
        break;
      }
    }
    if (s === undefined) {
      try {
        s = JSON.stringify(input);
      } catch {
        s = undefined;
      }
    }
  }
  return s && s.length > 200 ? `${s.slice(0, 200)}…` : s;
}

function readTrimmed(path: string): string | undefined {
  try {
    return readFileSync(path, "utf8").trim() || undefined;
  } catch {
    return undefined;
  }
}

function validAgentName(name: unknown): name is string {
  return typeof name === "string" && name.length > 0 && name.length <= 64 && !/[:/\s]/.test(name);
}

/**
 * A client for one agent. Create one per process and reuse it.
 *
 *   const shiplino = new Shiplino({ agent: "release-bot" });
 *   const session = shiplino.session({ title: "Cut the release" });
 */
export class Shiplino {
  readonly agent: string;
  private readonly agentVersion?: string;
  private readonly url?: string;
  private readonly token?: string;
  private readonly batchSize: number;
  private readonly maxQueue: number;
  private readonly timeoutMs: number;
  private readonly warn: (m: string) => void;
  private readonly warned = new Set<string>();
  private queue: WireEvent[] = [];
  private inflight?: Promise<void>;
  private timer?: ReturnType<typeof setInterval>;
  private enqueued = 0;
  private readonly intervalMs: number;
  /** No size-triggered sends before this time (ms), after a failed send. */
  private retryAt = 0;
  private exitFlushedAt = -1;
  private readonly onBeforeExit = () => {
    // One last attempt per batch of new events: beforeExit fires again
    // after the flush, and a down daemon must not keep the process alive.
    if (this.queue.length === 0 || this.exitFlushedAt === this.enqueued) return;
    this.exitFlushedAt = this.enqueued;
    void this.flush();
  };
  private readonly counters: Omit<Stats, "enabled" | "queued"> = {
    sent: 0,
    duplicates: 0,
    dropped: 0,
    rejected: 0,
  };
  readonly enabled: boolean;

  constructor(options: ShiplinoOptions) {
    const o = options ?? ({} as ShiplinoOptions);
    this.warn = o.onWarning ?? ((m) => console.warn(`shiplino: ${m}`));
    this.agent = typeof o.agent === "string" ? o.agent : "";
    this.agentVersion = o.agentVersion;
    this.batchSize = Math.max(1, Math.min(o.batchSize ?? 100, 1000));
    this.maxQueue = Math.max(this.batchSize, o.maxQueue ?? 10_000);
    this.timeoutMs = o.timeoutMs ?? 5000;
    this.intervalMs = o.flushIntervalMs ?? 1000;

    const home = o.home ?? process.env.SHIPLINO_HOME ?? join(homedir(), ".shiplino");
    this.token = o.token ?? readTrimmed(join(home, "token"));
    const port = readTrimmed(join(home, "port")) ?? "4777";
    this.url = (o.url ?? `http://127.0.0.1:${port}`).replace(/\/+$/, "");

    if (!validAgentName(o.agent)) {
      this.warnOnce("agent", `invalid agent name ${JSON.stringify(o.agent)}: use up to 64 characters without ":", "/" or spaces. Not recording.`);
      this.enabled = false;
    } else if (!this.token) {
      this.warnOnce("token", `no API token in ${join(home, "token")}: is Shiplino installed? Not recording.`);
      this.enabled = false;
    } else {
      this.enabled = true;
      this.timer = setInterval(() => void this.flush(), this.intervalMs);
      this.timer.unref?.();
      process.on("beforeExit", this.onBeforeExit);
    }
  }

  /** Starts a session (one task or run of your agent). */
  session(options: SessionOptions = {}): Session {
    const root: Root = {
      sessionId: `${this.agent}:${options.id ?? randomUUID()}`,
      instance: randomBytes(4).toString("hex"),
      seq: 0,
      cost: 0,
      cwd: options.cwd ?? safeCwd(),
    };
    const s = new Session(this, root, root.sessionId);
    s.emit("session.start", compact({ title: options.title, model: options.model }));
    return s;
  }

  get stats(): Stats {
    return { enabled: this.enabled, queued: this.queue.length, ...this.counters };
  }

  /** Sends everything queued now. Resolves when done; never rejects. */
  flush(): Promise<void> {
    if (!this.enabled) return Promise.resolve();
    if (!this.inflight) {
      this.inflight = this.drain().finally(() => {
        this.inflight = undefined;
      });
    }
    return this.inflight;
  }

  /** Flushes and stops the background timer. Call before process.exit(). */
  async close(): Promise<void> {
    if (this.timer) clearInterval(this.timer);
    this.timer = undefined;
    process.off("beforeExit", this.onBeforeExit);
    await this.flush();
    if (this.queue.length > 0) await this.flush(); // events queued during the last send
  }

  /** @internal */
  enqueue(e: WireEvent): void {
    if (!this.enabled) return;
    e.agent = this.agentVersion ? { name: this.agent, version: this.agentVersion } : { name: this.agent };
    this.queue.push(e);
    this.enqueued++;
    this.trim();
    if (this.queue.length >= this.batchSize && Date.now() >= this.retryAt) void this.flush();
  }

  private trim(): void {
    const over = this.queue.length - this.maxQueue;
    if (over > 0) {
      this.queue.splice(0, over);
      this.counters.dropped += over;
      this.warnOnce("dropped", `queue full (${this.maxQueue} events): dropping the oldest. See stats.dropped.`);
    }
  }

  private async drain(): Promise<void> {
    while (this.queue.length > 0) {
      const batch = this.queue.splice(0, this.batchSize);
      if (!(await this.send(batch))) {
        // Retry later, ahead of newer events.
        this.retryAt = Date.now() + this.intervalMs;
        this.queue.unshift(...batch);
        this.trim();
        return;
      }
    }
  }

  /** Sends one batch. False means "keep it and retry later". */
  private async send(batch: WireEvent[]): Promise<boolean> {
    let res: Response;
    try {
      res = await fetch(`${this.url}/api/v1/ingest`, {
        method: "POST",
        headers: { authorization: `Bearer ${this.token}`, "content-type": "application/json" },
        body: JSON.stringify(batch),
        signal: AbortSignal.timeout(this.timeoutMs),
      });
    } catch (err) {
      this.fail("unreachable", `daemon not reachable at ${this.url} (${errMessage(err)}); keeping events and retrying.`);
      return false;
    }
    let body: { accepted?: number; duplicates?: number; errors?: { index: number; error: string }[]; paused?: boolean; error?: string } = {};
    try {
      body = (await res.json()) as typeof body;
    } catch {
      // not JSON; the status decides
    }
    if (res.status >= 500 || res.status === 429) {
      this.fail("server", `daemon answered ${res.status}; keeping events and retrying.`);
      return false;
    }
    if (!res.ok && res.status !== 400) {
      // Bad token (401), too large (413) and the like: retrying won't help.
      this.counters.rejected += batch.length;
      this.fail(`status-${res.status}`, `daemon refused ${batch.length} events: ${res.status} ${body.error ?? ""}`.trim());
      return true;
    }
    const errors = body.errors ?? [];
    this.counters.sent += body.accepted ?? 0;
    this.counters.duplicates += body.duplicates ?? 0;
    this.counters.rejected += errors.length;
    if (errors.length > 0) {
      this.fail("invalid", `daemon rejected an event: ${errors[0].error}`);
    }
    if (body.paused) {
      this.counters.rejected += batch.length - errors.length;
      this.fail("paused", "recording is paused in Shiplino; events are not stored.");
    }
    return true;
  }

  private fail(kind: string, message: string): void {
    this.counters.lastError = message;
    this.warnOnce(kind, message);
  }

  private warnOnce(kind: string, message: string): void {
    if (this.warned.has(kind)) return;
    this.warned.add(kind);
    try {
      this.warn(message);
    } catch {
      // a broken warning handler must not break the agent
    }
  }
}

interface Root {
  sessionId: string;
  /** Random per Session object, so a resumed session id never reuses dedup keys. */
  instance: string;
  seq: number;
  cost: number;
  cwd?: string;
}

/** A tool call in progress. */
export class ToolCall {
  private readonly started = Date.now();
  private done = false;

  /** @internal */
  constructor(
    private readonly session: Session,
    readonly id: string,
    private readonly name: string,
  ) {}

  /** Records the end of the call; the duration is measured for you. */
  end(ok = true, error?: string): void {
    if (this.done) return;
    this.done = true;
    this.session.emit(
      "tool.end",
      compact({
        tool_call_id: this.id,
        tool: toolKind(this.name),
        tool_raw: this.name,
        ok,
        duration_ms: Date.now() - this.started,
        error,
      }),
    );
  }
}

/** A session, or a subagent of one. All methods are fire-and-forget. */
export class Session {
  private ended = false;
  private turnOpen = false;

  /** @internal */
  constructor(
    private readonly client: Shiplino,
    private readonly root: Root,
    /** This session's id (for subagents: "<session id>/sub:<id>"). */
    readonly id: string,
    private readonly parent?: Session,
    private readonly type?: string,
  ) {}

  /** The top-level session id this one belongs to. */
  get sessionId(): string {
    return this.root.sessionId;
  }

  /** Starts a turn (a prompt and the work it causes). Ends the previous turn. */
  turn(prompt: string): void {
    if (this.turnOpen) this.endTurn("ok");
    this.turnOpen = true;
    this.emit("turn.start", { prompt });
  }

  /** Ends the current turn. A session with file edits moves to Review, otherwise to Done. */
  endTurn(status: EndStatus = "ok", error?: string): void {
    this.turnOpen = false;
    this.emit("turn.end", compact({ status, error }));
  }

  /** Starts a tool call. Call .end() on the result when it finishes. */
  tool(name: string, input?: unknown): ToolCall {
    const id = `t${this.root.instance}-${++this.root.seq}`;
    const call = new ToolCall(this, id, name);
    this.emit("tool.start", compact({ tool_call_id: id, tool: toolKind(name), tool_raw: name, input_summary: summarize(input) }));
    return call;
  }

  /** Records a shell command that ran. */
  shell(command: string, exitCode: number, durationMs?: number): void {
    this.emit("shell.exec", compact({ command, exit_code: exitCode, duration_ms: durationMs }));
  }

  /** Records a file edit with the line counts your agent knows. */
  fileEdit(path: string, added = 0, removed = 0): void {
    this.emit("file.edit", { path, op: "edit", tool: "edit", lines_added: added, lines_removed: removed, lines_source: "reported" });
  }

  /** Records one model response's token usage (and cost, if your agent knows it). */
  usage(u: UsageOptions): void {
    const data: Data = compact({
      model: u.model,
      message_id: u.messageId,
      input_tokens: u.inputTokens ?? 0,
      output_tokens: u.outputTokens ?? 0,
      cache_read_tokens: u.cacheRead ?? 0,
      cache_write_tokens: u.cacheWrite ?? 0,
    });
    const cost = u.costUsd;
    if (typeof cost === "number" && Number.isFinite(cost) && cost >= 0) {
      data.cost_usd = cost;
      data.cost_source = "reported";
    }
    this.emit("usage", data);
    if (data.cost_usd !== undefined) {
      // The agent's own running total, so the session shows its cost as
      // reported rather than computed.
      this.root.cost += cost as number;
      this.emit(
        "usage",
        { report: true, cost_source: "reported", process: `sdk:${this.root.sessionId}:${this.root.instance}`, total_cost_usd: this.root.cost },
        true,
      );
    }
  }

  /** The agent is waiting for the user. */
  waiting(message: string): void {
    this.emit("waiting.start", { reason: "input", message });
  }

  /** The user answered; the agent continues. */
  resumed(): void {
    this.emit("waiting.end", { resolution: "resumed" });
  }

  /** Renames the session's card. */
  setTitle(title: string): void {
    this.emit("session.update", { title });
  }

  /** Starts a subagent and returns it as a child session. */
  subagent(type: string): Session {
    const childId = `${this.id}/sub:${randomBytes(6).toString("hex")}`;
    this.emit("subagent.start", { child_session_id: childId, agent_type: type });
    return new Session(this.client, this.root, childId, this, type);
  }

  /** Ends the session (or subagent). "error" marks it failed. */
  end(status: EndStatus = "ok", error?: string): void {
    if (this.ended) return;
    if (this.turnOpen || status === "error") this.endTurn(status, error);
    if (this.parent) {
      this.parent.emit("subagent.end", { child_session_id: this.id, agent_type: this.type, status: status === "ok" ? "done" : status });
    } else {
      this.emit("session.end", { status });
    }
    this.ended = true;
  }

  /** @internal */
  emit(kind: string, data: Data, atRoot = false): void {
    if (this.ended) return;
    try {
      const e: WireEvent = {
        id: `${this.root.instance}-${++this.root.seq}`,
        v: 1,
        ts: new Date().toISOString(),
        kind,
        agent: { name: "" },
        session_id: this.root.sessionId,
        data,
      };
      if (this.root.cwd) e.project = { cwd: this.root.cwd };
      if (this.parent && !atRoot) {
        e.actor_id = this.id;
        e.parent_actor = this.parent.id;
        e.actor_type = this.type;
      }
      this.client.enqueue(e);
    } catch {
      // never throw into the agent
    }
  }
}

function compact(o: Data): Data {
  for (const k of Object.keys(o)) if (o[k] === undefined || o[k] === null) delete o[k];
  return o;
}

function safeCwd(): string | undefined {
  try {
    return process.cwd();
  } catch {
    return undefined;
  }
}

function errMessage(err: unknown): string {
  const e = err as { cause?: { code?: string }; message?: string };
  return e?.cause?.code ?? e?.message ?? String(err);
}
