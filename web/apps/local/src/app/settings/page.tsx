"use client";

import {
  ArrowUpCircle,
  Bell,
  CircleAlert,
  CircleCheck,
  CircleDashed,
  Cloud,
  CloudOff,
  Download,
  Pause,
  Play,
  Radio,
  Server,
  Shield,
  Wallet,
} from "lucide-react";
import { type ReactNode, useCallback, useEffect, useState } from "react";
import { toast } from "sonner";
import { AgentDot } from "@/components/common/agent-dot";
import { Empty } from "@/components/common/empty";
import { AgentChange } from "@/components/settings/agent-change";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { API_BASE, api, type Settings, type SyncState } from "@/lib/api";
import { formatAgo, formatDuration } from "@/lib/format";

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
  full: "Everything Shiplino captures, including tool output and file edit diffs (capped).",
};

function formatBytes(n: number): string {
  if (n < 1024 * 1024) return `${Math.max(1, Math.round(n / 1024))} KB`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`;
}

function SyncCard({ sync }: { sync: SyncState }) {
  const on = sync.enabled && sync.signed_in;
  return (
    <Card className="gap-3">
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          {on ? <Cloud className="size-4" aria-hidden /> : <CloudOff className="size-4" aria-hidden />}
          {on ? "Sync is on" : "Sync is off"}
        </CardTitle>
        <CardDescription>
          {on
            ? "Events from allowed projects are uploaded to your workspace, stripped to the sync capture level and redacted again first."
            : "Nothing leaves this machine. Sync is opt-in, and only projects you allow are ever sent."}
        </CardDescription>
      </CardHeader>
      <CardContent>
        {sync.signed_in ? (
          <>
            <Row label="Signed in as">
              {sync.account || "—"}
              {sync.role && <span className="text-muted-foreground text-xs"> · {sync.role}</span>}
            </Row>
            <Row label="Workspace">
              {sync.workspace_name || sync.workspace_id}{" "}
              <span className="text-muted-foreground text-xs">
                · <Mono>{sync.endpoint}</Mono>
              </span>
            </Row>
            <Row label="Credentials">
              {sync.credential_store === "file" ? (
                <>
                  <span className="text-status-waiting">In a file only you can read</span>
                  <p className="text-muted-foreground text-xs">{sync.credential_note}</p>
                </>
              ) : (
                "In the OS keychain"
              )}
            </Row>
          </>
        ) : (
          <Row label="Account">{sync.needs_login ? "Signed out by the sync service" : "Not signed in"}</Row>
        )}
        <Row label="What is sent">
          <span className="font-medium">Metadata only</span>
          <p className="text-muted-foreground text-xs">
            Sessions, status, timing, tokens and cost, tool names, outcomes, project-relative file paths and git
            references. Never prompts, replies, commands, tool output, diffs or file contents.
            {sync.send_titles ? " Session titles are sent too (send_titles)." : " Session titles stay here."}
          </p>
          {sync.ignored?.map((n) => (
            <p key={n} className="text-status-waiting text-xs">
              {n}
            </p>
          ))}
        </Row>
        <Row label="Allowed projects">
          {sync.projects.length ? (
            <div className="flex flex-wrap gap-1">
              {sync.projects.map((p) => (
                <Badge key={p} variant="secondary" className="max-w-full font-mono" title={p}>
                  <span className="truncate">{p}</span>
                </Badge>
              ))}
            </div>
          ) : (
            <span className="text-muted-foreground">None, so nothing is sent</span>
          )}
        </Row>
        {sync.exclude.length > 0 && (
          <Row label="Excluded">
            <div className="flex flex-wrap gap-1">
              {sync.exclude.map((p) => (
                <Badge key={p} variant="outline" className="max-w-full font-mono" title={p}>
                  <span className="truncate">{p}</span>
                </Badge>
              ))}
            </div>
          </Row>
        )}
        {sync.signed_in && (
          <>
            <Row label="Last sync">
              {formatAgo(sync.last_upload)}
              <span className="text-muted-foreground text-xs"> · {sync.uploaded.toLocaleString()} events uploaded</span>
              {sync.rejected > 0 && (
                <span className="text-status-waiting text-xs">
                  {" "}
                  · {sync.rejected.toLocaleString()} refused by the service as invalid
                </span>
              )}
            </Row>
            <Row label="Backlog">
              {sync.backlog > 0 ? `${sync.backlog.toLocaleString()} events to check` : "Up to date"}
            </Row>
          </>
        )}
        {sync.last_error && (
          <Row label="Problem">
            <span className="flex items-start gap-1.5 text-status-failed">
              <CircleAlert className="mt-0.5 size-3.5 shrink-0" aria-hidden />
              <span className="break-words">{sync.last_error}</span>
            </span>
            {sync.next_retry && (
              <p className="text-muted-foreground text-xs">
                Retrying at {new Date(sync.next_retry).toLocaleTimeString()}
              </p>
            )}
          </Row>
        )}
        <p className="pt-2 text-muted-foreground text-xs">
          Manage it in a terminal: <Mono>shiplino sync login</Mono>, <Mono>shiplino sync allow &lt;project&gt;</Mono>,{" "}
          <Mono>shiplino sync status --dry-run</Mono> (shows exactly what would be sent).
        </p>
      </CardContent>
    </Card>
  );
}

function UpdateRow({ s }: { s: Settings }) {
  const u = s.update;
  if (!u) return null;
  if (u.dev) return <span className="text-muted-foreground">Development build, not updated from releases</span>;
  const last = u.checked_at ? ` · last checked ${formatAgo(u.checked_at)}` : "";
  if (!u.check) {
    return (
      <>
        Checked only when you run <Mono>shiplino update</Mono>
        <span className="text-muted-foreground text-xs">{last}</span>
        <p className="text-muted-foreground text-xs">
          Daily checks ask api.github.com, so they're off until you set <Mono>[update] check = true</Mono>.
        </p>
      </>
    );
  }
  return (
    <>
      {u.auto_install ? "Checked daily and installed automatically" : "Checked daily"}
      <span className="text-muted-foreground text-xs">
        {u.channel ? ` · ${u.channel} channel` : ""}
        {last}
      </span>
      {u.error && <p className="text-status-waiting text-xs">Last check failed: {u.error}</p>}
    </>
  );
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

      {s.update?.available && (
        <Card className="gap-3 border-primary/40">
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <ArrowUpCircle className="size-4 text-primary" aria-hidden /> Shiplino {s.update.available} is available
            </CardTitle>
            <CardDescription>
              You have {s.version}. Run <Mono>shiplino update</Mono> in a terminal: it verifies the download, keeps this
              version for <Mono>shiplino update --rollback</Mono> and restarts the daemon.
              {s.update.url && (
                <>
                  {" "}
                  <a className="underline underline-offset-2" href={s.update.url} target="_blank" rel="noreferrer">
                    What's new
                  </a>
                </>
              )}
            </CardDescription>
          </CardHeader>
          {s.update.install_error && (
            <CardContent>
              <p className="text-status-waiting text-xs">
                Installing it automatically failed: {s.update.install_error}. Your agents kept running the old version.
              </p>
            </CardContent>
          )}
        </Card>
      )}

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
              hint = "Connect again to point it at this one";
            } else if (a.found) {
              badge = <Badge variant="outline">Not connected</Badge>;
            } else {
              badge = (
                <Badge variant="ghost" className="text-muted-foreground">
                  <CircleDashed /> Not installed
                </Badge>
              );
            }
            return (
              <div key={a.name} className="flex flex-col gap-1 border-b py-3 last:border-0">
                <div className="flex items-center gap-2 text-sm">
                  <AgentDot agent={a.id} />
                  <span className="font-medium">{a.name}</span>
                  {a.version && <span className="font-mono text-muted-foreground text-xs">{a.version}</span>}
                  <span className="ml-auto flex items-center gap-2">
                    {badge}
                    {a.found && !a.problem && !(a.connected && a.current) && (
                      <AgentChange agent={a} action="connect" onDone={setS} />
                    )}
                    {a.connected && <AgentChange agent={a} action="remove" onDone={setS} />}
                  </span>
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

      <Card id="privacy" className="scroll-mt-16 gap-3">
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

      {s.sync && <SyncCard sync={s.sync} />}

      <Card id="budgets" className="scroll-mt-16 gap-3">
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <Wallet className="size-4" aria-hidden /> Budgets
          </CardTitle>
          <CardDescription>
            {s.budget.spends.length === 0
              ? "No spend limits set."
              : "Spend at list prices, counted on the day a session started. You're notified at 80% and when a budget is reached."}
          </CardDescription>
        </CardHeader>
        <CardContent>
          {s.budget.spends.map((b) => {
            const pct = b.limit_usd > 0 ? Math.min(100, (b.spent_usd / b.limit_usd) * 100) : 0;
            const tone = pct >= 100 ? "bg-status-failed" : pct >= 80 ? "bg-status-waiting" : "bg-primary/70";
            return (
              <Row key={b.scope} label={b.label}>
                <div className="flex items-center gap-3">
                  <div className="h-1.5 flex-1 rounded-full bg-foreground/10" aria-hidden>
                    <div className={`h-full rounded-full ${tone}`} style={{ width: `${pct}%` }} />
                  </div>
                  <span className="shrink-0 font-mono text-xs tabular-nums">
                    ${b.spent_usd.toFixed(2)} of ${b.limit_usd.toFixed(2)}
                  </span>
                </div>
              </Row>
            );
          })}
          <Row label="Daily digest">{s.budget.digest ? `At ${s.budget.digest}` : "Off"}</Row>
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
          <Row label="A plan usage window fills up">
            {s.notify.limit_percent > 0 ? `On, at ${s.notify.limit_percent}% (once per window)` : "Off"}
          </Row>
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
          <Row label="Updates">
            <UpdateRow s={s} />
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
