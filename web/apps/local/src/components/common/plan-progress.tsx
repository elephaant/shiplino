import { cn } from "cn";
import { ListChecks } from "lucide-react";

/** PlanProgress is a small "3/7" bar for the agent's own todo list. */
export function PlanProgress({ done, total, className }: { done: number; total: number; className?: string }) {
  if (!total) return null;
  const pct = Math.min(100, Math.round((done / total) * 100));
  return (
    <div
      className={cn("flex items-center gap-1.5 text-xs text-muted-foreground", className)}
      title={`The agent's todo list: ${done} of ${total} done`}
    >
      <ListChecks className="size-3.5 shrink-0" aria-hidden />
      <div
        role="progressbar"
        aria-label="Todo list progress"
        aria-valuemin={0}
        aria-valuemax={total}
        aria-valuenow={done}
        className="h-1.5 flex-1 overflow-hidden rounded-full bg-muted"
      >
        <div
          className="h-full rounded-full bg-status-done transition-[width] motion-reduce:transition-none"
          style={{ width: `${pct}%` }}
        />
      </div>
      <span className="font-mono tabular-nums">
        {done}/{total}
      </span>
    </div>
  );
}
