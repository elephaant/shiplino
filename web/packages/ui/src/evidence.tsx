"use client";

import { CircleQuestionMark, Eye, type LucideIcon, MessageSquareQuote, Sigma } from "lucide-react";
import { Tooltip as TooltipPrimitive } from "radix-ui";

/**
 * Where a value comes from:
 * - reported: the agent said so (its own cost, diff, title, limits)
 * - observed: Shiplino saw it happen (hooks, transcripts, git)
 * - inferred: Shiplino derived it (idle detection, likely commit links, estimates)
 * - unknown: nothing tells us
 */
export type Evidence = "reported" | "observed" | "inferred" | "unknown";

/** A value's evidence plus one sentence on its specific source. */
export interface EvidenceInfo {
  evidence: Evidence;
  detail?: string;
}

// Each kind has its own icon and border style, so it never relies on color.
const kinds: Record<Evidence, { label: string; meaning: string; icon: LucideIcon; border: string }> = {
  reported: { label: "Reported", meaning: "The agent said so.", icon: MessageSquareQuote, border: "border-solid" },
  observed: { label: "Observed", meaning: "Shiplino saw it happen.", icon: Eye, border: "border-solid" },
  inferred: { label: "Inferred", meaning: "Shiplino derived it.", icon: Sigma, border: "border-dashed" },
  unknown: { label: "Unknown", meaning: "Nothing records it.", icon: CircleQuestionMark, border: "border-dotted" },
};

/**
 * EvidenceBadge says where a value comes from, with a tooltip that explains
 * the source. Its accessible name carries the label and the detail.
 * `compact` shows only the icon; use it inside dense rows such as board
 * cards.
 */
export function EvidenceBadge({
  evidence,
  detail,
  compact = false,
  className = "",
}: EvidenceInfo & { compact?: boolean; className?: string }) {
  const k = kinds[evidence];
  const Icon = k.icon;
  return (
    <TooltipPrimitive.Root>
      <TooltipPrimitive.Trigger asChild>
        {/* A button, so keyboard users can focus it to open the tooltip. */}
        <button
          type="button"
          aria-label={detail ? `${k.label}: ${detail}` : k.label}
          data-evidence={evidence}
          className={`inline-flex shrink-0 items-center gap-0.5 rounded-full border ${k.border} border-muted-foreground/40 font-sans text-[10px] font-medium leading-4 text-muted-foreground tracking-normal normal-case cursor-help outline-none focus-visible:ring-2 focus-visible:ring-ring ${compact ? "p-0.5" : "px-1.5"} ${className}`}
        >
          <Icon className="size-3 shrink-0" aria-hidden />
          {!compact && k.label}
        </button>
      </TooltipPrimitive.Trigger>
      <TooltipPrimitive.Portal>
        <TooltipPrimitive.Content
          side="top"
          sideOffset={4}
          className="z-50 max-w-72 rounded-md bg-foreground px-3 py-1.5 text-xs text-background text-balance"
        >
          <span className="font-medium">{k.label}:</span> {k.meaning}
          {detail && <span className="mt-0.5 block opacity-80">{detail}</span>}
          <TooltipPrimitive.Arrow className="fill-foreground" />
        </TooltipPrimitive.Content>
      </TooltipPrimitive.Portal>
    </TooltipPrimitive.Root>
  );
}
