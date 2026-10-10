// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

"use client";

import {
  Bell,
  CircleAlert,
  CircleCheck,
  CircleDashed,
  Download,
  Pause,
  Play,
  Radio,
  Server,
  Shield,
} from "lucide-react";
import { type ReactNode, useCallback, useEffect, useState } from "react";
import { toast } from "sonner";
import { AgentDot } from "@/components/common/agent-dot";
import { Empty } from "@/components/common/empty";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { API_BASE, api, type Settings } from "@/lib/api";
import { formatDuration } from "@/lib/format";

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex flex-col gap-1 border-b py-2.5 text-sm last:border-0 sm:flex-row sm:items-center sm:gap-4">
      <span className="w-44 shrink-0 text-muted-foreground">{label}</span>
      <div className="min-w-0 flex-1">{children}</div>
    </div>
  );
}

function Mono({ children }: { children: ReactNode }) {
  return <code className="break-all font-mono text-xs">{children}</code>;
}

const levels: Record<string, string> = {
  minimal: "Timing, tool names, file paths, exit codes, tokens and cost. No prompts, commands or outputs.",
  standard: "Also prompts (truncated), commands and short summaries.",
  full: "Everything Shiplino captures, including tool output (capped).",
};

function formatBytes(n: number): string {
  if (n < 1024 * 1024) return `${Math.max(1, Math.round(n / 1024))} KB`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`;
}

export default function SettingsPage() {
  const [s, setS] = useState<Settings | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(() => {
    api<Settings>("/api/v1/settings")
      .then((d) => {
        setS(d);
        setError(null);
      })
      .catch((e: Error) => setError(e.message));
  }, []);
  useEffect(load, [load]);

  const act = async (path: string, body?: object, ok?: string) => {
    setBusy(true);
    try {
      const r = await api<Settings | undefined>(path, {
        method: "POST",
        body: body ? JSON.stringify(body) : undefined,
      });
      if (r) setS(r);
      if (ok) toast.success(ok);
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  if (error && !s) {
    return (
      <Empty icon={Radio} title="Can't reach the Shiplino daemon">
        Run <code className="font-mono">shiplino doctor</code> in a terminal.
      </Empty>
    );
  }
  if (!s) return <Skeleton className="h-96 w-full" />;

  const restartNote = (
    <p className="text-muted-foreground text-xs">
      To change these, edit <Mono>{s.config_path}</Mono> and restart the daemon.
    </p>
  );

  return (
    <div className="flex max-w-4xl flex-col gap-4">
      <div>
        <h1 className="font-semibold text-xl tracking-tight">Settings</h1>
        <p className="text-muted-foreground text-sm">Recording, agents, privacy and notifications on this machine.</p>
      </div>

      <Card className="gap-3">
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            {s.paused ? (
              <Pause className="size-4 text-status-waiting" aria-hidden />
            ) : (
              <span className="size-2.5 rounded-full bg-status-running" aria-hidden />
            )}
            {s.paused ? "Recording is paused" : "Recording"}
          </CardTitle>
          <CardDescription>
            {s.paused
              ? s.paused_until
                ? `Paused until ${new Date(s.paused_until).toLocaleString()}. Agents keep working; nothing is recorded.`
                : "Paused until you resume. Agents keep working; nothing is recorded."
              : "Every connected agent is recorded. Pausing stops recording without touching your agents."}
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-wrap gap-2">
          {s.paused ? (
            <Button size="sm" disabled={busy} onClick={() => act("/api/v1/resume", undefined, "Recording resumed")}>
              <Play /> Resume
            </Button>
          ) : (
            <>
              <Button
                size="sm"
                variant="outline"
                disabled={busy}
                onClick={() => act("/api/v1/pause", { minutes: 60 }, "Paused for 1 hour")}
              >
                <Pause /> Pause for 1 hour
              </Button>
              <Button
                size="sm"
                variant="outline"
                disabled={busy}
                onClick={() => act("/api/v1/pause", {}, "Paused until you resume")}
              >
                <Pause /> Pause until I resume
              </Button>
            </>
          )}
        </CardContent>
      </Card>

      <Card className="gap-3">
        <CardHeader>
          <CardTitle>Agents</CardTitle>
          <CardDescription>
            Shiplino connects through each agent's own hooks. They print nothing and add no tokens.
          </CardDescription>
        </CardHeader>
        <CardContent>
          {s.agents.map((a) => {
            let badge: ReactNode;
            let hint: ReactNode = null;
            if (a.problem) {
              badge = (
                <Badge variant="destructive">
                  <CircleAlert /> Problem
                </Badge>
              );
              hint = a.problem;
            } else if (a.connected && a.current) {
              badge = (
                <Badge variant="secondary">
                  <CircleCheck className="text-status-done" /> Connected
                </Badge>
              );
            } else if (a.connected) {
              badge = <Badge variant="outline">Points at another Shiplino</Badge>;
              hint = (
                <>
                  Run <Mono>shiplino doctor --fix</Mono>
                </>
              );
            } else if (a.found) {
              badge = <Badge variant="outline">Not connected</Badge>;
              hint = (
                <>
                  Run <Mono>shiplino setup</Mono> to connect it
                </>
              );
            } else {
              badge = (
                <Badge variant="ghost" className="text-muted-foreground">
                  <CircleDashed /> Not installed
                </Badge>
              );
            }
            return (
              <div key={a.id} className="flex flex-col gap-1 border-b py-3 last:border-0">
                <div className="flex items-center gap-2 text-sm">
                  <AgentDot agent={a.id} />
                  <span className="font-medium">{a.name}</span>
                  {a.version && <span className="font-mono text-muted-foreground text-xs">{a.version}</span>}
                  <span className="ml-auto">{badge}</span>
                </div>
                {(a.found || hint) && (
                  <div className="flex flex-wrap gap-x-4 pl-4.5 text-muted-foreground text-xs">
                    {a.found && a.hooks_path && <Mono>{a.hooks_path}</Mono>}
                    {hint && <span>{hint}</span>}
                  </div>
                )}
              </div>
            );
          })}
        </CardContent>
      </Card>

      <Card className="gap-3">
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <Shield className="size-4" aria-hidden /> Privacy
          </CardTitle>
          <CardDescription>
            Everything stays on this machine. Secrets are redacted before anything is stored.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <Row label="Capture level">
            <span className="font-medium capitalize">{s.capture_level}</span>
            <p className="text-muted-foreground text-xs">{levels[s.capture_level]}</p>
          </Row>
          <Row label="Extra redaction rules">{s.extra_redaction_patterns || "None (built-in rules only)"}</Row>
          <Row label="Data">
            <Mono>{s.home}</Mono> <span className="text-muted-foreground text-xs">· {formatBytes(s.data_bytes)}</span>
          </Row>
          <div className="pt-2">{restartNote}</div>
        </CardContent>
      </Card>

      <Card className="gap-3">
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <Bell className="size-4" aria-hidden /> Notifications
          </CardTitle>
          <CardDescription>
            {!s.notify.enabled
              ? "Turned off."
              : s.notify.available
                ? `Desktop notifications via ${s.notify.via}.`
                : "No notification service was found on this system."}
          </CardDescription>
        </CardHeader>
        <CardContent>
          <Row label="An agent waits on you">
            {s.notify.enabled && s.notify.waiting ? "On (after 3 seconds)" : "Off"}
          </Row>
          <Row label="A turn finishes">
            {s.notify.enabled && s.notify.finished
              ? `On, for turns over ${formatDuration(s.notify.min_turn_ms)}`
              : "Off"}
          </Row>
          <Row label="A session fails">{s.notify.enabled && s.notify.failed ? "On" : "Off"}</Row>
          <div className="flex flex-wrap items-center justify-between gap-2 pt-3">
            {restartNote}
            <Button
              size="sm"
              variant="outline"
              disabled={busy || !s.notify.available}
              onClick={() => act("/api/v1/notify/test", undefined, "Test notification sent")}
            >
              <Bell /> Send a test notification
            </Button>
          </div>
        </CardContent>
      </Card>

      <Card className="gap-3">
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <Server className="size-4" aria-hidden /> Daemon
          </CardTitle>
        </CardHeader>
        <CardContent>
          <Row label="Version">
            <Mono>{s.version}</Mono>
          </Row>
          <Row label="Address">
            <Mono>http://localhost:{s.port}</Mono>
            {s.port !== 4777 && (
              <span className="text-status-waiting text-xs">
                {" "}
                · port 4777 was busy; bookmarks of :4777 won't reach it
              </span>
            )}
          </Row>
          <Row label="Since it started">
            {s.health.events.toLocaleString()} events from {s.health.lines.toLocaleString()} lines
            {(s.health.bad > 0 || s.health.unknown > 0) && (
              <span className="text-muted-foreground text-xs">
                {" "}
                · {s.health.bad} unreadable, {s.health.unknown} unknown
              </span>
            )}
          </Row>
          <Row label="Backlog">
            {s.health.spool_backlog_bytes > 0 ? formatBytes(s.health.spool_backlog_bytes) : "Up to date"}
          </Row>
          {s.health.watch_error && (
            <Row label="File watching">
              <span className="text-status-waiting">Unavailable, polling instead.</span>
              <p className="text-muted-foreground text-xs">
                Run <Mono>shiplino doctor</Mono> for the fix ({s.health.watch_error}).
              </p>
            </Row>
          )}
        </CardContent>
      </Card>

      <Card className="gap-3">
        <CardHeader>
          <CardTitle>Your data</CardTitle>
          <CardDescription>Every session, with its time, tokens, cost and changes.</CardDescription>
        </CardHeader>
        <CardContent className="flex flex-wrap gap-2">
          <Button size="sm" variant="outline" asChild>
            <a href={`${API_BASE}/api/v1/export?format=csv`} download>
              <Download /> Export CSV
            </a>
          </Button>
          <Button size="sm" variant="outline" asChild>
            <a href={`${API_BASE}/api/v1/export?format=json`} download>
              <Download /> Export JSON
            </a>
          </Button>
        </CardContent>
      </Card>
    </div>
  );
}
