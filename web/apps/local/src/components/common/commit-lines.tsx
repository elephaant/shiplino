import { EvidenceBadge } from "@shiplino/ui/evidence";
import type { Link } from "@/lib/api";
import { authorshipEvidence } from "@/lib/evidence";

/** "N of M lines by agent" for a commit, or the files the agent edited when lines can't be split. */
export function commitLinesText(l: Link): string | null {
  if (!l.authorship) return null; // recorded before line authorship existed
  const total = l.lines ?? 0;
  if (l.authorship === "unknown") {
    const files = l.agent_files ?? 0;
    return `${files} file${files === 1 ? "" : "s"} edited by the agent · ${total} lines, split unknown`;
  }
  const unknown = l.unknown_lines ? ` · ${l.unknown_lines} unknown` : "";
  return `${l.agent_lines ?? 0} of ${total} lines by agent${unknown}`;
}

/** A commit's line authorship with its evidence badge. */
export function CommitLines({ link, className = "" }: { link: Link; className?: string }) {
  const text = commitLinesText(link);
  if (!text) return null;
  return (
    <span className={`flex items-center gap-1.5 text-xs text-muted-foreground ${className}`}>
      <span className="font-mono tabular-nums">{text}</span>
      <EvidenceBadge {...authorshipEvidence(link.authorship)} />
    </span>
  );
}
