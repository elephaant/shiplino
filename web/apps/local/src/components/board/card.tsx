"use client";

import { EvidenceBadge } from "@shiplino/ui/evidence";
import { cn } from "cn";
import {
  FileText,
  GitBranch,
  GitCommitHorizontal,
  Hand,
  Pin,
  RotateCcw,
  Search,
  SquarePen,
  Terminal,
} from "lucide-react";
import { AgentDot } from "@/components/common/agent-dot";
import { PlanProgress } from "@/components/common/plan-progress";
import { PRBadge } from "@/components/common/pr-badge";
import { Badge } from "@/components/ui/badge";
import type { BoardCard } from "@/lib/api";
import { commitEvidence, costEvidence, linesEvidence, statusEvidence } from "@/lib/evidence";
import { formatCost, formatDuration, waitingLabel } from "@/lib/format";
import { API_EQUIVALENT, onPlan, useLimits } from "@/lib/limits";
import { isUnseen, useSeen } from "@/lib/seen";

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
  const limits = useLimits();
  const { seen, markSeen } = useSeen();
  const unseen = waiting && isUnseen(seen, card.id, card.waiting_since);
  const open = () => {
    if (waiting) markSeen(card.id, card.waiting_since);
    onOpen?.();
  };
  const subs = card.subagents ?? [];
  const shown =
    subs.length > 3 ? subs.filter((s) => s.status === "running" || s.status === "waiting").slice(0, 3) : subs;
  const prs = (card.links ?? []).filter((l) => l.kind === "pr");
  const commits = (card.links ?? []).filter((l) => l.kind === "commit");
  // Dense cards badge the cost always, and other values only when Shiplino
  // inferred them; the card sheet and session page show every source.
  const lines = linesEvidence(card.lines_source);
  const guessedCommit = commits.find((c) => c.action !== "exact");
  return (
    // The whole card is the control: the drag library won't start a drag
    // inside a <button>, so the card can't be one and can't contain one.
    // biome-ignore lint/a11y/useSemanticElements: see above; keyboard and role handled here
    <div
      role="button"
      tabIndex={0}
      onClick={open}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          open();
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
        {unseen && (
          <span
            className="mt-1.5 ml-auto size-2 shrink-0 rounded-full bg-primary"
            role="img"
            aria-label="New: not opened since it started waiting"
            title="New: not opened since it started waiting"
          />
        )}
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
        <p className="flex items-center gap-1 text-muted-foreground text-xs">
          Went quiet: no activity for a while
          <EvidenceBadge compact {...statusEvidence({ status: "idle" })} />
        </p>
      )}

      {waiting && (
        <Badge variant="outline" className="w-fit border-status-waiting/60 px-1.5 py-0 font-normal text-xs">
          <Hand className="size-3 text-status-waiting" aria-hidden />
          {waitingLabel(card.waiting_reason)}
        </Badge>
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

      {card.plan_total ? <PlanProgress done={card.plan_done ?? 0} total={card.plan_total} /> : null}

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
          <span className="flex items-center gap-0.5">
            {card.usage === "none" ? (
              <span className="font-sans">no cost data</span>
            ) : (
              <span title={onPlan(limits, card.agent) ? API_EQUIVALENT : undefined}>{formatCost(card.cost_usd)}</span>
            )}
            {(card.usage === "none" || card.cost_usd > 0) && <EvidenceBadge compact {...costEvidence(card)} />}
          </span>
          {card.files > 0 && (
            <span className="flex items-center gap-0.5">
              {card.files}f <span className="text-status-done">+{card.lines_added}</span>{" "}
              <span className="text-status-failed">−{card.lines_removed}</span>
              {lines?.evidence === "inferred" && <EvidenceBadge compact {...lines} />}
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
              {guessedCommit && <EvidenceBadge compact {...commitEvidence(guessedCommit.action)} />}
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
