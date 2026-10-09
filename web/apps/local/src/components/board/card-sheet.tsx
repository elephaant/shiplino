// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

"use client";

import { ExternalLink, PinOff, Trash2 } from "lucide-react";
import Link from "next/link";
import { toast } from "sonner";
import { AgentDot } from "@/components/common/agent-dot";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { api, type BoardCard } from "@/lib/api";
import { agentName, formatCost, formatDuration } from "@/lib/format";

function Stat({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-0.5">
      <span className="text-xs text-muted-foreground">{label}</span>
      <span className="font-mono text-sm tabular-nums">{value}</span>
    </div>
  );
}

export function CardSheet({
  card,
  onClose,
  onChanged,
}: {
  card: BoardCard | null;
  onClose: () => void;
  onChanged: () => void;
}) {
  const act = async (init: RequestInit, done: string) => {
    if (!card) return;
    try {
      await api(`/api/v1/cards/${encodeURIComponent(card.id)}`, init);
      toast.success(done);
      onChanged();
      onClose();
    } catch (e) {
      toast.error((e as Error).message);
    }
  };
  return (
    <Sheet open={card != null} onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full gap-0 sm:max-w-md">
        {card && (
          <>
            <SheetHeader>
              <SheetTitle className="flex items-start gap-2 pr-6 leading-snug">
                {card.agent && <AgentDot agent={card.agent} className="mt-1.5" />}
                {card.title}
              </SheetTitle>
              <SheetDescription>
                {card.origin === "auto"
                  ? `${agentName(card.agent ?? "")}${card.model ? ` · ${card.model}` : ""}${card.branch ? ` · ${card.branch}` : ""}`
                  : "Manual card"}
              </SheetDescription>
            </SheetHeader>
            <div className="flex flex-col gap-5 px-4 pb-4">
              {card.origin === "auto" && (
                <div className="grid grid-cols-3 gap-3">
                  <Stat label="Duration" value={formatDuration(card.duration_ms)} />
                  <Stat
                    label={card.cost_source === "reported" ? "Cost (reported)" : "Cost"}
                    value={formatCost(card.cost_usd)}
                  />
                  <Stat label="Waited for you" value={formatDuration(card.waiting_ms ?? 0)} />
                  <Stat label="Files" value={card.files} />
                  <Stat
                    label="Lines"
                    value={
                      <>
                        <span className="text-status-done">+{card.lines_added}</span>{" "}
                        <span className="text-status-failed">−{card.lines_removed}</span>
                      </>
                    }
                  />
                  <Stat
                    label="Sprint"
                    value={
                      card.rolled_over_from ? `${card.sprint} (from ${card.rolled_over_from})` : card.sprint || "—"
                    }
                  />
                </div>
              )}
              {card.now_doing && <p className="rounded-md bg-muted p-2 text-sm">{card.now_doing}</p>}
              {(card.subagents?.length ?? 0) > 0 && (
                <div className="flex flex-col gap-1.5">
                  <h3 className="text-xs font-medium text-muted-foreground">Subagents</h3>
                  {card.subagents?.map((s) => (
                    <div key={s.id} className="flex items-center gap-2 text-sm">
                      <Badge variant="secondary">{s.status}</Badge>
                      <span className="font-medium">{s.type || "subagent"}</span>
                      <span className="truncate text-muted-foreground">{s.now_doing}</span>
                      <span className="ml-auto font-mono text-xs">{formatCost(s.cost_usd)}</span>
                    </div>
                  ))}
                </div>
              )}
              {(card.links ?? [])
                .filter((l) => l.kind === "pr")
                .map((l) => (
                  <a
                    key={l.url}
                    href={l.url}
                    target="_blank"
                    rel="noreferrer"
                    className="flex items-center gap-1 text-sm text-primary hover:underline"
                  >
                    <ExternalLink className="size-3.5" aria-hidden /> Pull request #{l.number}
                  </a>
                ))}
              {card.notes && <p className="whitespace-pre-wrap text-sm">{card.notes}</p>}
              <div className="flex flex-wrap gap-2">
                {card.origin === "auto" && (
                  <Button asChild size="sm">
                    <Link href={`/session/?id=${encodeURIComponent(card.id)}`}>Open timeline</Link>
                  </Button>
                )}
                {card.origin === "auto" && card.pinned && (
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() =>
                      act({ method: "PATCH", body: JSON.stringify({ pinned: false }) }, "Following the agent again")
                    }
                  >
                    <PinOff className="size-3.5" /> Unpin
                  </Button>
                )}
                {card.origin === "manual" && (
                  <Button size="sm" variant="outline" onClick={() => act({ method: "DELETE" }, "Card deleted")}>
                    <Trash2 className="size-3.5" /> Delete
                  </Button>
                )}
              </div>
            </div>
          </>
        )}
      </SheetContent>
    </Sheet>
  );
}
