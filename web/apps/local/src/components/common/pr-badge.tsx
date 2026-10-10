// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

import { cn } from "cn";
import { Check, Circle, GitMerge, GitPullRequest, GitPullRequestClosed, GitPullRequestDraft, X } from "lucide-react";
import type { Link } from "@/lib/api";

// State comes from the GitHub integration when it's on; without it the
// badge is a plain link (no state is guessed).
const looks: Record<string, { icon: typeof GitPullRequest; tone: string; label: string }> = {
  open: { icon: GitPullRequest, tone: "text-status-done", label: "open" },
  draft: { icon: GitPullRequestDraft, tone: "text-muted-foreground", label: "draft" },
  merged: { icon: GitMerge, tone: "text-status-review", label: "merged" },
  closed: { icon: GitPullRequestClosed, tone: "text-status-failed", label: "closed" },
};

const checkLooks: Record<string, { icon: typeof Check; tone: string }> = {
  success: { icon: Check, tone: "text-status-done" },
  failure: { icon: X, tone: "text-status-failed" },
  pending: { icon: Circle, tone: "text-status-waiting" },
};

export function PRBadge({ pr }: { pr: Link }) {
  const look = (pr.state && looks[pr.state]) || { icon: GitPullRequest, tone: "", label: "" };
  const checks = pr.checks ? checkLooks[pr.checks] : undefined;
  const title = [
    `#${pr.number}`,
    pr.title,
    look.label,
    pr.checks && `checks ${pr.checks}`,
    pr.review?.replace("_", " "),
  ]
    .filter(Boolean)
    .join(" · ");
  return (
    <a
      href={pr.url}
      target="_blank"
      rel="noreferrer"
      title={title}
      onClick={(e) => e.stopPropagation()}
      className="flex items-center gap-0.5 rounded bg-secondary px-1 text-secondary-foreground hover:underline"
    >
      <look.icon className={cn("size-3", look.tone)} aria-label={look.label || "pull request"} />#{pr.number}
      {checks && <checks.icon className={cn("size-3", checks.tone)} aria-label={`checks ${pr.checks}`} />}
      {pr.review === "approved" && <Check className="size-3 text-status-done" aria-label="approved" />}
      {pr.review === "changes_requested" && <X className="size-3 text-status-waiting" aria-label="changes requested" />}
    </a>
  );
}
