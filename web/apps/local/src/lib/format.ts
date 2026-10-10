// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

export function formatCost(usd: number | undefined): string {
  if (!usd) return "—";
  if (usd < 0.01) return "<$0.01";
  return `$${usd.toFixed(2)}`;
}

export function formatDuration(ms: number): string {
  if (!ms || ms < 0) return "0s";
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.round(s / 60);
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  return `${h}h ${m % 60}m`;
}

export function formatAgo(iso: string | undefined, now = Date.now()): string {
  if (!iso) return "never";
  const s = Math.max(0, (now - Date.parse(iso)) / 1000);
  if (s < 60) return "just now";
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 172800) return `${Math.floor(s / 3600)}h ago`;
  return `${Math.floor(s / 86400)}d ago`;
}

export function formatTokens(n: number): string {
  if (n < 1000) return String(n);
  if (n < 1_000_000) return `${(n / 1000).toFixed(n < 10_000 ? 1 : 0)}k`;
  return `${(n / 1_000_000).toFixed(1)}M`;
}

const agentNames: Record<string, string> = {
  "claude-code": "Claude Code",
  codex: "Codex",
  cursor: "Cursor",
  "gemini-cli": "Gemini CLI",
  "copilot-cli": "Copilot CLI",
  windsurf: "Windsurf",
  cline: "Cline",
  opencode: "OpenCode",
  aider: "Aider",
};

export function agentName(id: string): string {
  return agentNames[id] ?? id;
}

const agentTokens: Record<string, string> = {
  "claude-code": "claude",
  codex: "codex",
  cursor: "cursor",
  "gemini-cli": "gemini",
  "copilot-cli": "copilot",
  windsurf: "windsurf",
};

// Full class names, so Tailwind sees them in the source.
const agentClasses: Record<string, string> = {
  claude: "bg-agent-claude",
  codex: "bg-agent-codex",
  cursor: "bg-agent-cursor",
  gemini: "bg-agent-gemini",
  copilot: "bg-agent-copilot",
  windsurf: "bg-agent-windsurf",
  other: "bg-agent-other",
};

/** Tailwind classes for an agent's identity color. */
export function agentColor(id: string): string {
  return agentClasses[agentTokens[id] ?? "other"] ?? "bg-agent-other";
}

/** CSS color value of an agent's identity color (for charts). */
export function agentVar(id: string): string {
  return `var(--agent-${agentTokens[id] ?? "other"})`;
}

/** Percent change from prev to cur, or undefined when there's no base. */
export function change(cur: number, prev: number): number | undefined {
  if (!prev) return undefined;
  return ((cur - prev) / prev) * 100;
}
