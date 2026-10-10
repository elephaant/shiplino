"use client";

import { CircleAlert, Link2, Unlink } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Skeleton } from "@/components/ui/skeleton";
import { type AgentPlan, type AgentStatus, api, type Settings } from "@/lib/api";

const lineTone: Record<string, string> = {
  "+": "text-status-done",
  "-": "text-status-failed",
  "@": "text-muted-foreground",
  "\\": "text-muted-foreground",
};

/** A unified diff as plain text: each line is a text node, never HTML. */
function DiffView({ diff }: { diff: string }) {
  const lines = diff.replace(/\n$/, "").split("\n");
  return (
    <pre className="max-h-[50vh] overflow-auto rounded-md border bg-muted/40 p-3 font-mono text-xs leading-relaxed">
      {lines.map((l, i) => {
        // The ---/+++ file header comes first; only later lines are changes.
        const tone = i < 2 ? "text-muted-foreground" : (lineTone[l.charAt(0)] ?? "");
        return (
          // biome-ignore lint/suspicious/noArrayIndexKey: diff lines are static text with no identity but their position
          <span key={i} className={`block whitespace-pre ${tone}`}>
            {l || " "}
          </span>
        );
      })}
    </pre>
  );
}

const hookWords = {
  connect: "Connect",
  remove: "Remove",
  removeButton: "Remove hooks",
  connected: "connected",
  removed: "hooks removed",
  connectAbout:
    "Shiplino adds its hook to this file. It prints nothing, adds no tokens and can't block the agent. The old file is backed up first.",
  removeAbout:
    "Shiplino removes only its own hooks. Everything else in the file stays, and the old file is backed up first.",
  upToDate: "the hooks are already up to date.",
  notFound: "no Shiplino hooks were found.",
};

/** The same flow for an opt-in extra (the Claude Code status line wrapper). */
const optInWords = {
  connect: "Turn on",
  remove: "Turn off",
  removeButton: "Turn off",
  connected: "on",
  removed: "off",
  connectAbout:
    "Shiplino wraps your status line command: it records the plan usage numbers Claude Code passes to it, then runs your command, so what you see stays exactly the same. It adds no tokens. The old file is backed up first.",
  removeAbout: "Shiplino puts your own status line command back exactly as it was. The old file is backed up first.",
  upToDate: "it is already on.",
  notFound: "it is already off.",
};

/**
 * Connect (or remove) an agent's hooks from the settings page. The dialog
 * first shows the exact diff, made by the same code as `shiplino setup
 * --dry-run`; nothing is written until the user confirms.
 */
export function AgentChange({
  agent,
  action,
  onDone,
}: {
  agent: AgentStatus;
  action: "connect" | "remove";
  onDone: (s: Settings) => void;
}) {
  const [open, setOpen] = useState(false);
  const [plan, setPlan] = useState<AgentPlan | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const path = `/api/v1/agents/${encodeURIComponent(agent.key)}`;
  const connect = action === "connect";

  const onOpenChange = (o: boolean) => {
    setOpen(o);
    if (!o) return;
    setPlan(null);
    setError(null);
    api<AgentPlan>(`${path}/preview?action=${action}`)
      .then(setPlan)
      .catch((e: Error) => setError(e.message));
  };

  const apply = async () => {
    setBusy(true);
    try {
      onDone(await api<Settings>(`${path}/${action}`, { method: "POST" }));
      toast.success(`${agent.name} ${connect ? w.connected : w.removed}`, {
        description: connect ? plan?.note : undefined,
      });
      setOpen(false);
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const nothing = plan && plan.changes.length === 0;
  const w = agent.opt_in ? optInWords : hookWords;
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogTrigger asChild>
        <Button size="sm" variant={connect ? "default" : "outline"}>
          {connect ? <Link2 /> : <Unlink />} {connect ? w.connect : w.remove}
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>
            {connect ? w.connect : w.remove} {agent.name}
          </DialogTitle>
          <DialogDescription>{connect ? w.connectAbout : w.removeAbout}</DialogDescription>
        </DialogHeader>
        {error && (
          <p className="flex items-start gap-1.5 text-sm text-status-failed">
            <CircleAlert className="mt-0.5 size-3.5 shrink-0" aria-hidden />
            <span className="break-words">{error}</span>
          </p>
        )}
        {!plan && !error && <Skeleton className="h-40 w-full" />}
        {plan?.changes.map((c) => (
          <div key={c.path} className="flex min-w-0 flex-col gap-1.5">
            <code className="break-all font-mono text-muted-foreground text-xs">{c.path}</code>
            <DiffView diff={c.diff} />
          </div>
        ))}
        {nothing && !plan.problem && (
          <p className="text-muted-foreground text-sm">Nothing to change: {connect ? w.upToDate : w.notFound}</p>
        )}
        {plan?.problem && (
          <p className="flex items-start gap-1.5 text-sm text-status-waiting">
            <CircleAlert className="mt-0.5 size-3.5 shrink-0" aria-hidden />
            <span className="break-words">{plan.problem}</span>
          </p>
        )}
        {plan?.note && plan.changes.length > 0 && <p className="text-muted-foreground text-xs">{plan.note}.</p>}
        <DialogFooter>
          <Button variant="outline" onClick={() => setOpen(false)}>
            Cancel
          </Button>
          <Button
            variant={connect ? "default" : "destructive"}
            disabled={busy || !plan || plan.changes.length === 0}
            onClick={apply}
          >
            {connect ? w.connect : w.removeButton}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
