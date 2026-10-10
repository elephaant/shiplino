// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

"use client";

import {
  Bot,
  CircleCheck,
  CircleX,
  Coins,
  Copy,
  FileText,
  GitBranch,
  GitCommitHorizontal,
  GitPullRequest,
  Hand,
  MessageSquare,
  Play,
  Radio,
  Search,
  SquarePen,
  Terminal,
  Wrench,
} from "lucide-react";
import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { Suspense, useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import { AgentDot } from "@/components/common/agent-dot";
import { Empty } from "@/components/common/empty";
import { DiffView } from "@/components/session/diff-view";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { type AgentEvent, api, type Session } from "@/lib/api";
import { agentName, formatCost, formatDuration, formatTokens, noUsageReason } from "@/lib/format";
import { useLive } from "@/lib/live";

const str = (d: Record<string, unknown> | undefined, k: string) => (typeof d?.[k] === "string" ? (d[k] as string) : "");
const num = (d: Record<string, unknown> | undefined, k: string) => (typeof d?.[k] === "number" ? (d[k] as number) : 0);

/** describe turns an event into an icon and one line of text, or null to hide it. */
function describe(
  e: AgentEvent,
  ended: Set<string>,
  rel: (p: string) => string,
): { icon: React.ElementType; text: string; tone?: string } | null {
  const d = e.data;
  switch (e.kind) {
    case "session.start":
      return { icon: Play, text: `Session started${str(d, "model") ? ` · ${str(d, "model")}` : ""}` };
    case "turn.start":
      return { icon: MessageSquare, text: str(d, "prompt") || "Prompt" };
    case "tool.start": {
      // Finished calls are shown through their result events; show only calls still in progress.
      if (ended.has(str(d, "tool_call_id"))) return null;
      const tool = str(d, "tool");
      const summary = ["edit", "write", "read"].includes(tool) ? rel(str(d, "input_summary")) : str(d, "input_summary");
      const icon =
        tool === "shell"
          ? Terminal
          : tool === "read"
            ? FileText
            : tool === "search"
              ? Search
              : tool === "edit" || tool === "write"
                ? SquarePen
                : Wrench;
      return { icon, text: `${summary || str(d, "tool_raw")} · running…` };
    }
    case "file.read":
      return { icon: FileText, text: `Read ${rel(str(d, "path"))}` };
    case "file.edit": {
      const op = str(d, "op");
      const verb = op === "create" ? "Created" : op === "delete" ? "Deleted" : "Edited";
      // Some agents don't report line counts (e.g. a delete): show none
      // rather than a misleading "+0 −0".
      const counts =
        d?.lines_added == null && d?.lines_removed == null
          ? ""
          : `  +${num(d, "lines_added")} −${num(d, "lines_removed")}`;
      return { icon: SquarePen, text: `${verb} ${rel(str(d, "path"))}${counts}` };
    }
    case "shell.exec": {
      const code = d?.exit_code;
      return { icon: Terminal, text: str(d, "command"), tone: code === 0 ? "ok" : code == null ? undefined : "fail" };
    }
    case "tool.end":
      if (["shell", "edit", "write", "read", "mcp"].includes(str(d, "tool"))) return null; // shown via derived events
      return {
        icon: str(d, "tool") === "search" ? Search : Wrench,
        text: `${str(d, "tool")} ${d?.ok ? "" : "failed"}`,
        tone: d?.ok ? undefined : "fail",
      };
    case "mcp.call":
      return { icon: Wrench, text: `MCP ${str(d, "server")}/${str(d, "tool")}` };
    case "waiting.start":
      return { icon: Hand, text: str(d, "message") || "Waiting for you", tone: "wait" };
    case "subagent.start":
      return { icon: Bot, text: `Subagent ${str(d, "agent_type") || ""} started` };
    case "subagent.end":
      return { icon: Bot, text: `Subagent ${str(d, "agent_type") || ""} finished` };
    case "turn.end":
      return str(d, "status") === "error"
        ? { icon: CircleX, text: `Turn failed: ${str(d, "error")}`, tone: "fail" }
        : { icon: CircleCheck, text: str(d, "assistant_summary") || "Turn finished", tone: "ok" };
    case "git.pr":
      return { icon: GitPullRequest, text: `Pull request #${num(d, "number")} ${str(d, "action")}` };
    case "git.commit":
      return {
        icon: GitCommitHorizontal,
        text: `Commit ${str(d, "sha").slice(0, 7)} ${str(d, "message")} (${str(d, "attribution")})`,
        tone: "ok",
      };
    case "git.push":
      return { icon: GitBranch, text: `Pushed ${str(d, "branch")}` };
    case "session.end":
      return { icon: CircleCheck, text: `Session ended (${str(d, "reason")})` };
    default:
      return null;
  }
}

const tones: Record<string, string> = {
  ok: "text-status-done",
  fail: "text-status-failed",
  wait: "text-status-waiting",
};

function Stat({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-0.5">
      <span className="text-xs text-muted-foreground">{label}</span>
      <span className="font-mono text-sm tabular-nums">{value}</span>
    </div>
  );
}

function SessionPage() {
  const id = useSearchParams().get("id");
  const version = useLive((s) => s.version);
  const [session, setSession] = useState<Session | null>(null);
  const [events, setEvents] = useState<AgentEvent[]>([]);
  const [error, setError] = useState<string | null>(null);

  // biome-ignore lint/correctness/useExhaustiveDependencies: refetch on live changes
  useEffect(() => {
    if (!id) return;
    const enc = encodeURIComponent(id);
    Promise.all([
      api<Session>(`/api/v1/sessions/${enc}`),
      api<{ events: AgentEvent[] }>(`/api/v1/sessions/${enc}/events?limit=1000`),
    ])
      .then(([s, e]) => {
        setSession(s);
        setEvents(e.events);
        setError(null);
      })
      .catch((err: Error) => setError(err.message));
  }, [id, version]);

  const ended = useMemo(() => {
    const set = new Set<string>();
    for (const e of events)
      if (e.kind === "tool.end" && typeof e.data?.tool_call_id === "string") set.add(e.data.tool_call_id as string);
    return set;
  }, [events]);

  const actors = useMemo(() => {
    const m = new Map<string, string>();
    for (const e of events)
      if (e.actor_id && e.actor_id !== e.session_id) m.set(e.actor_id, e.actor_type || "subagent");
    return m;
  }, [events]);

  if (!id) return <Empty icon={Radio} title="No session selected" />;
  if (error && !session)
    return (
      <Empty icon={Radio} title="Couldn't load this session">
        {error}
      </Empty>
    );
  if (!session) return <Skeleton className="h-96 w-full" />;

  const commands = events.filter((e) => e.kind === "shell.exec");
  const root = session.cwd ? `${session.cwd.replace(/\/$/, "")}/` : "";
  const rel = (p: string) => (root && p.startsWith(root) ? p.slice(root.length) : p);
  const usage = events.filter((e) => e.kind === "usage" && !e.data?.report);
  const end = session.ended_at && !session.ended_at.startsWith("0001") ? session.ended_at : session.last_event_at;
  const resume = session.agent === "claude-code" ? `claude --resume ${session.id.split(":")[1]}` : "";

  return (
    <div className="flex min-w-0 flex-col gap-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <h1 className="flex items-center gap-2 text-xl font-semibold tracking-tight">
            <AgentDot agent={session.agent} />
            <span className="truncate">{session.title || session.id}</span>
          </h1>
          <p className="mt-1 text-sm text-muted-foreground">
            {agentName(session.agent)}
            {session.agent_version && ` ${session.agent_version}`}
            {session.model && ` · ${session.model}`}
            {session.branch && ` · ${session.branch}`}
            {session.project_id && (
              <>
                {" · "}
                <Link href={`/board/?project=${encodeURIComponent(session.project_id)}`} className="hover:underline">
                  {session.project_id}
                </Link>
              </>
            )}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Badge variant="secondary">{session.status === "waiting" ? "waiting on you" : session.status}</Badge>
          {resume && (
            <Button
              size="sm"
              variant="outline"
              onClick={() => navigator.clipboard.writeText(resume).then(() => toast.success("Resume command copied"))}
            >
              <Copy className="size-3.5" /> Copy resume command
            </Button>
          )}
        </div>
      </div>

      <Card className="py-4">
        <CardContent className="grid grid-cols-2 gap-4 sm:grid-cols-4 lg:grid-cols-8">
          <Stat label="Duration" value={formatDuration(Date.parse(end) - Date.parse(session.started_at))} />
          <Stat
            label={session.cost_source === "reported" ? "Cost (reported)" : "Cost"}
            value={
              session.usage === "none" ? (
                <span className="font-sans text-muted-foreground" title={noUsageReason(session.agent)}>
                  no cost data
                </span>
              ) : (
                formatCost(session.best_cost_usd)
              )
            }
          />
          <Stat
            label="Tokens in / out"
            value={
              session.usage === "none"
                ? "—"
                : `${formatTokens(session.input_tokens + session.cache_read_tokens + session.cache_write_tokens)} / ${formatTokens(session.output_tokens)}`
            }
          />
          <Stat label="Turns" value={session.turns} />
          <Stat
            label="Tool calls"
            value={session.tool_errors ? `${session.tool_calls} (${session.tool_errors} failed)` : session.tool_calls}
          />
          <Stat label="Files" value={session.files?.length ?? 0} />
          <Stat label="Lines" value={`+${session.lines_added} −${session.lines_removed}`} />
          <Stat label="Waited for you" value={formatDuration(session.waiting_ms)} />
        </CardContent>
      </Card>

      <Tabs defaultValue="timeline">
        <TabsList>
          <TabsTrigger value="timeline">Timeline</TabsTrigger>
          <TabsTrigger value="files">Changes ({session.files?.length ?? 0})</TabsTrigger>
          <TabsTrigger value="commands">Commands ({commands.length})</TabsTrigger>
          <TabsTrigger value="usage">Usage</TabsTrigger>
        </TabsList>

        <TabsContent value="timeline">
          <Card className="py-2">
            <CardContent className="flex flex-col px-2">
              {events.length === 0 && <p className="p-4 text-sm text-muted-foreground">No events yet.</p>}
              {events.map((e) => {
                const line = describe(e, ended, rel);
                if (!line) return null;
                const Icon = line.icon;
                const actor = e.actor_id ? actors.get(e.actor_id) : undefined;
                return (
                  <div key={e.id} className="flex items-start gap-3 rounded-md px-2 py-1.5 text-sm hover:bg-accent/40">
                    <span className="w-16 shrink-0 pt-0.5 font-mono text-xs tabular-nums text-muted-foreground">
                      {new Date(e.ts).toLocaleTimeString([], {
                        hour: "2-digit",
                        minute: "2-digit",
                        second: "2-digit",
                        hour12: false,
                      })}
                    </span>
                    <Icon
                      className={`mt-0.5 size-4 shrink-0 ${line.tone ? tones[line.tone] : "text-muted-foreground"}`}
                      aria-hidden
                    />
                    {actor && (
                      <Badge variant="outline" className="shrink-0">
                        {actor}
                      </Badge>
                    )}
                    <span
                      className={`min-w-0 break-words ${e.kind === "shell.exec" ? "font-mono text-xs leading-5" : ""}`}
                    >
                      {line.text}
                    </span>
                  </div>
                );
              })}
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="files">
          <DiffView
            sessionId={session.id}
            version={version}
            rel={rel}
            actorLabel={(a) => actors.get(a) ?? "subagent"}
          />
        </TabsContent>

        <TabsContent value="commands">
          <Card className="py-2">
            <CardContent className="flex flex-col px-2">
              {commands.map((e) => {
                const code = e.data?.exit_code;
                return (
                  <div key={e.id} className="flex items-center gap-3 rounded-md px-2 py-1.5 hover:bg-accent/40">
                    <span
                      className={`w-10 shrink-0 font-mono text-xs ${code === 0 ? "text-status-done" : code == null ? "text-muted-foreground" : "text-status-failed"}`}
                    >
                      {code == null ? "—" : `exit ${code}`}
                    </span>
                    <code className="min-w-0 flex-1 truncate font-mono text-xs">{str(e.data, "command")}</code>
                    <span className="font-mono text-xs text-muted-foreground">
                      {formatDuration(num(e.data, "duration_ms"))}
                    </span>
                    <Button
                      size="icon"
                      variant="ghost"
                      className="size-7"
                      aria-label="Copy command"
                      onClick={() =>
                        navigator.clipboard.writeText(str(e.data, "command")).then(() => toast.success("Copied"))
                      }
                    >
                      <Copy className="size-3.5" />
                    </Button>
                  </div>
                );
              })}
              {commands.length === 0 && <p className="p-2 text-sm text-muted-foreground">No shell commands.</p>}
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="usage">
          <Card className="py-2">
            <CardContent className="overflow-x-auto px-2">
              {session.usage === "none" && (
                <p className="flex items-center gap-2 px-2 py-2 text-xs text-muted-foreground">
                  <Coins className="size-3.5" aria-hidden /> {noUsageReason(session.agent)}
                </p>
              )}
              {session.cost_source === "reported" && (
                <p className="flex items-center gap-2 px-2 py-2 text-xs text-muted-foreground">
                  <Coins className="size-3.5" aria-hidden /> The total uses the agent&apos;s own cost report, which
                  includes background calls not listed below.
                </p>
              )}
              <table className="w-full text-xs">
                <thead className="text-left text-muted-foreground">
                  <tr>
                    <th className="px-2 py-1 font-medium">Time</th>
                    <th className="px-2 py-1 font-medium">Model</th>
                    <th className="px-2 py-1 text-right font-medium">In</th>
                    <th className="px-2 py-1 text-right font-medium">Cache read</th>
                    <th className="px-2 py-1 text-right font-medium">Cache write</th>
                    <th className="px-2 py-1 text-right font-medium">Out</th>
                    <th className="px-2 py-1 text-right font-medium">Cost</th>
                  </tr>
                </thead>
                <tbody className="font-mono tabular-nums">
                  {usage.map((e) => (
                    <tr key={e.id} className="border-t">
                      <td className="px-2 py-1">{new Date(e.ts).toLocaleTimeString([], { hour12: false })}</td>
                      <td className="px-2 py-1">{str(e.data, "model")}</td>
                      <td className="px-2 py-1 text-right">{formatTokens(num(e.data, "input_tokens"))}</td>
                      <td className="px-2 py-1 text-right">{formatTokens(num(e.data, "cache_read_tokens"))}</td>
                      <td className="px-2 py-1 text-right">{formatTokens(num(e.data, "cache_write_tokens"))}</td>
                      <td className="px-2 py-1 text-right">{formatTokens(num(e.data, "output_tokens"))}</td>
                      <td className="px-2 py-1 text-right">{formatCost(num(e.data, "cost_usd"))}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
              {usage.length === 0 && (
                <p className="p-2 text-sm text-muted-foreground">
                  No token usage recorded yet (it comes from the agent&apos;s transcript).
                </p>
              )}
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>
    </div>
  );
}

export default function Page() {
  return (
    <Suspense>
      <SessionPage />
    </Suspense>
  );
}
