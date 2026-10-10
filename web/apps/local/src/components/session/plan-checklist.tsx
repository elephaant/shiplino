import { cn } from "cn";
import { Ban, Circle, CircleCheck, CircleSlash, LoaderCircle } from "lucide-react";
import { PlanProgress } from "@/components/common/plan-progress";
import { Card, CardContent } from "@/components/ui/card";
import type { PlanItem } from "@/lib/api";

const statusIcon: Record<string, { icon: React.ElementType; className: string; label: string }> = {
  completed: { icon: CircleCheck, className: "text-status-done", label: "Done" },
  in_progress: { icon: LoaderCircle, className: "text-status-running", label: "In progress" },
  cancelled: { icon: CircleSlash, className: "text-muted-foreground", label: "Cancelled" },
  blocked: { icon: Ban, className: "text-status-waiting", label: "Blocked" },
};
const pending = { icon: Circle, className: "text-muted-foreground", label: "To do" };

/** PlanChecklist shows the agent's own todo list, as the agent last wrote it. */
export function PlanChecklist({ items, done, total }: { items: PlanItem[]; done: number; total: number }) {
  const withText = items.filter((i) => i.text);
  return (
    <Card className="gap-2 py-3">
      <CardContent className="flex flex-col gap-2 px-4">
        <div className="flex items-center gap-3">
          <h2 className="shrink-0 text-sm font-medium">Agent's todo list</h2>
          <PlanProgress done={done} total={total} className="max-w-xs flex-1" />
        </div>
        {withText.length > 0 ? (
          <ul className="flex flex-col gap-1">
            {withText.map((item, i) => {
              const s = statusIcon[item.status ?? ""] ?? pending;
              return (
                <li key={item.id ?? i} className="flex items-start gap-2 text-sm">
                  <s.icon className={cn("mt-0.5 size-3.5 shrink-0", s.className)} aria-label={s.label} />
                  <span
                    className={cn(
                      item.status === "completed" && "text-muted-foreground",
                      item.status === "cancelled" && "text-muted-foreground line-through",
                    )}
                  >
                    {item.text}
                  </span>
                </li>
              );
            })}
          </ul>
        ) : (
          <p className="text-xs text-muted-foreground">
            Item text isn't recorded at the minimal capture level, only the progress.
          </p>
        )}
      </CardContent>
    </Card>
  );
}
