// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

import { cn } from "cn";
import { agentColor, agentName } from "@/lib/format";

export function AgentDot({ agent, className }: { agent: string; className?: string }) {
  return (
    <span
      className={cn("inline-block size-2.5 shrink-0 rounded-full", agentColor(agent), className)}
      title={agentName(agent)}
      aria-hidden
    />
  );
}
