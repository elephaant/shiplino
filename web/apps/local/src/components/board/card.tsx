"use client";

import { cn } from "cn";
import { FileText, GitBranch, GitCommitHorizontal, Pin, RotateCcw, Search, SquarePen, Terminal } from "lucide-react";
import { AgentDot } from "@/components/common/agent-dot";
import { PRBadge } from "@/components/common/pr-badge";
import { Badge } from "@/components/ui/badge";
import type { BoardCard } from "@/lib/api";
import { formatCost, formatDuration, noUsageReason } from "@/lib/format";

const statusDot: Record<string, string> = {
  running: "bg-status-running",
  waiting: "bg-status-waiting animate-pulse",
  review: "bg-status-review",
  done: "bg-status-done",
  failed: "bg-status-failed",
  idle: "bg-muted-foreground/40",
};

function DoingIcon({ text }: { text: string }) {
  const cls = "size-3.5 shrink-0";
  if (text.startsWith("Editing")) return <SquarePen className={cls} aria-hidden />;
  if (text.startsWith("Reading")) return <FileText className={cls} aria-hidden />;
  if (text.startsWith("Running")) return <Terminal className={cls} aria-hidden />;
  if (text.startsWith("Searching")) return <Search className={cls} aria-hidden />;
  return null;
}

export function CardView({ card, dragging, onOpen }: { card: BoardCard; dragging?: boolean; onOpen?: () => void }) {
  const waiting = card.status === "waiting";
  const subs = card.subagents ?? [];
  const shown =
    subs.length > 3 ? subs.filter((s) => s.status === "running" || s.status === "waiting").slice(0, 3) : subs;
  const prs = (card.links ?? []).filter((l) => l.kind === "pr");
  const commits = (card.links ?? []).filter((l) => l.kind === "commit");
  return (
    // The whole card is the control: the drag library won't start a drag
    // inside a <button>, so the card can't be one and can't contain one.
    // biome-ignore lint/a11y/useSemanticElements: see above; keyboard and role handled here
    <div
      role="button"
      tabIndex={0}
      onClick={onOpen}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          onOpen?.();
        }
      }}
      className={cn(
        "group flex cursor-pointer flex-col gap-1.5 rounded-lg border bg-card p-3 text-left text-sm shadow-xs transition-shadow focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none",
        waiting && "border-status-waiting/70",
        dragging ? "shadow-lg ring-2 ring-ring/40" : "hover:shadow-sm",
      )}
    >
      <div className="flex items-start gap-2">
        {card.agent ? (
          <AgentDot agent={card.agent} className="mt-1" />
        ) : (
          <span className="mt-1 size-2.5 shrink-0 rounded-full border" />
        )}
        <span className="line-clamp-2 font-medium leading-snug">{card.title}</span>
      </div>

      {card.origin === "auto" && (
        <div className="flex items-center gap-1 text-xs text-muted-foreground">
          <span className={cn("size-1.5 shrink-0 rounded-full", statusDot[card.status ?? "idle"])} aria-hidden />
          {card.branch ? (
            <span className="flex min-w-0 items-center gap-1">
              <GitBranch className="size-3 shrink-0" aria-hidden />
              <span className="truncate font-mono">{card.branch}</span>
            </span>
          ) : (
            <span>{card.status}</span>
          )}
        </div>
      )}

      {card.status === "idle" && (
        <p className="text-muted-foreground text-xs" title="No activity for 30 minutes: the agent may have been closed">
          Went quiet: no activity for a while
        </p>
      )}

      {card.now_doing && (
        <div
          className={cn(
            "flex items-center gap-1.5 text-xs",
            waiting ? "font-medium text-foreground" : "text-muted-foreground",
          )}
        >
          <DoingIcon text={card.now_doing} />
          <span className="truncate">{card.now_doing}</span>
        </div>
      )}

      {shown.length > 0 && (
        <ul className="flex flex-col gap-0.5 border-l pl-2">
          {shown.map((s) => (
            <li key={s.id} className="flex items-center gap-1.5 text-xs">
              <span className={cn("size-1.5 shrink-0 rounded-full", statusDot[s.status])} aria-hidden />
              <span className="shrink-0 font-medium">{s.type || "subagent"}</span>
              <span className="truncate text-muted-foreground">{s.now_doing ?? s.status}</span>
            </li>
          ))}
          {subs.length > shown.length && (
            <li className="text-xs text-muted-foreground">+ {subs.length - shown.length} subagents</li>
          )}
        </ul>
      )}

      {card.origin === "auto" && (
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1 font-mono text-xs tabular-nums text-muted-foreground">
          <span>{formatDuration(card.duration_ms)}</span>
          {card.usage === "none" ? (
            <Badge
              variant="outline"
              className="px-1.5 py-0 font-sans font-normal text-muted-foreground"
              title={noUsageReason(card.agent)}
            >
              no cost data
            </Badge>
          ) : (
            <span
              title={card.cost_source === "reported" ? "Reported by the agent" : "Computed from tokens × list prices"}
            >
              {formatCost(card.cost_usd)}
            </span>
          )}
          {card.files > 0 && (
            <span>
              {card.files}f <span className="text-status-done">+{card.lines_added}</span>{" "}
              <span className="text-status-failed">−{card.lines_removed}</span>
            </span>
          )}
          {prs.map((p) => (
            <PRBadge key={p.url} pr={p} />
          ))}
          {commits.length > 0 && (
            <span
              className="flex items-center gap-0.5"
              title={commits.map((c) => `${c.ref?.slice(0, 7)} ${c.message ?? ""}`).join("\n")}
            >
              <GitCommitHorizontal className="size-3" aria-hidden />
              {commits.length}
            </span>
          )}
          <span className="ml-auto flex items-center gap-1">
            {card.rolled_over_from ? (
              <span title={`Rolled over from sprint ${card.rolled_over_from}`}>
                <RotateCcw className="size-3" aria-label="Rolled over" />
              </span>
            ) : null}
            {card.pinned && (
              <span title="Pinned: moved by hand">
                <Pin className="size-3" aria-label="Pinned" />
              </span>
            )}
          </span>
        </div>
      )}
    </div>
  );
}
