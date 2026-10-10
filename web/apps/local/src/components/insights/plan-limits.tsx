import { EvidenceBadge } from "@shiplino/ui/evidence";
import { Gauge } from "lucide-react";
import { AgentDot } from "@/components/common/agent-dot";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { limitEvidence } from "@/lib/evidence";
import { agentName, formatCost, formatTokens } from "@/lib/format";
import { describeWindow, formatReset, type LimitWindow, windowLabel, windowValue } from "@/lib/limits";

/** Bar tone: the threshold colors are always next to the number. */
export function limitTone(w: LimitWindow): string {
  if (w.reached || (w.used_percent ?? 0) >= 100) return "text-status-failed";
  if ((w.used_percent ?? 0) >= 80) return "text-status-waiting";
  return "";
}

function Row({ w }: { w: LimitWindow }) {
  const pct = w.reached ? 100 : w.used_percent;
  const tone = limitTone(w);
  return (
    <div className="flex flex-col gap-1.5" title={describeWindow(w)}>
      <div className="flex min-w-0 items-center gap-2 whitespace-nowrap text-sm">
        <AgentDot agent={w.agent} />
        <span className="truncate font-medium">{agentName(w.agent)}</span>
        <span className="text-muted-foreground">{windowLabel(w)}</span>
        <EvidenceBadge {...limitEvidence(w.source)} />
        <span className={`ml-auto shrink-0 font-mono tabular-nums ${tone}`}>
          {w.source === "estimate" ? `${formatTokens(w.tokens ?? 0)} tokens` : windowValue(w)}
        </span>
      </div>
      {pct !== undefined && (
        <div
          className="h-1.5 w-full rounded-full bg-muted"
          role="progressbar"
          aria-valuenow={Math.round(pct)}
          aria-valuemin={0}
          aria-valuemax={100}
          aria-label={`${agentName(w.agent)} ${windowLabel(w)} limit used`}
        >
          <div
            className={`h-full rounded-full ${tone ? "bg-current" : "bg-primary/70"} ${tone}`}
            style={{ width: `${Math.min(100, Math.max(pct, 2))}%` }}
          />
        </div>
      )}
      <div className="flex gap-3 font-mono text-muted-foreground text-xs tabular-nums">
        {w.resets_at ? (
          <span>resets {formatReset(w.resets_at)}</span>
        ) : (
          <span>{w.source === "estimate" && w.window === "7d" ? "last 7 days" : "no active window"}</span>
        )}
        {w.source === "estimate" && !!w.cost_usd && (
          <span title="At API list prices">{formatCost(w.cost_usd)} API-equivalent</span>
        )}
        {w.plan && <span>{w.plan} plan</span>}
      </div>
    </div>
  );
}

/** Plan usage windows (5-hour, weekly) per agent, with reset times. */
export function PlanLimits({ windows }: { windows: LimitWindow[] }) {
  return (
    <Card className="gap-3" id="limits">
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Gauge className="size-4 text-muted-foreground" aria-hidden />
          Plan limits
        </CardTitle>
        <CardDescription>
          Usage windows of flat-rate plans. Percentages are the agent's own; estimates sum tokens, since plan quotas
          aren't published.
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
        {windows.map((w) => (
          <Row key={`${w.agent}:${w.limit_id ?? ""}:${w.window_minutes}`} w={w} />
        ))}
      </CardContent>
    </Card>
  );
}
