import type { EvidenceInfo } from "@shiplino/ui/evidence";
import type { Status } from "@/lib/api";
import { agentName, noUsageReason } from "@/lib/format";

// Maps the provenance fields the API returns to evidence badges. Each
// function says which badge a value gets and why, in one sentence.

/** Cost: the agent's own figure, our token × price estimate, or nothing. */
export function costEvidence(v: {
  cost_source?: string;
  usage?: string;
  agent?: string;
  telemetry?: { cost_usd: number };
  reported_cost_usd?: number;
}): EvidenceInfo {
  if (v.usage === "none") return { evidence: "unknown", detail: noUsageReason(v.agent) };
  if (v.cost_source === "reported") {
    const viaTelemetry = (v.telemetry?.cost_usd ?? 0) > (v.reported_cost_usd ?? 0);
    return {
      evidence: "reported",
      detail: viaTelemetry
        ? "The agent's own cost, from the usage it exports over OpenTelemetry."
        : "The agent's own cost accounting.",
    };
  }
  if (v.cost_source === "computed")
    return { evidence: "inferred", detail: "Estimated by Shiplino: token counts × the model's list prices." };
  if (v.usage === "tokens")
    return { evidence: "unknown", detail: "Tokens were recorded, but the model has no known price." };
  return { evidence: "unknown", detail: "No usage recorded yet." };
}

/** Session status: seen through hooks or transcripts, except idle (a guess from silence). */
export function statusEvidence(v: { status: Status; hook_seen?: boolean; activity_source?: string }): EvidenceInfo {
  if (v.status === "idle")
    return {
      evidence: "inferred",
      detail:
        "No activity for 30 minutes (2 hours while a tool runs), so Shiplino assumes the agent was closed. The next event brings it back.",
    };
  if (v.hook_seen) return { evidence: "observed", detail: "Seen live through the agent's hooks." };
  // Custom agents send their own events: their status is what they say.
  if (v.activity_source === "http")
    return { evidence: "reported", detail: "Sent by the agent itself to Shiplino's ingest API." };
  const via: Record<string, string> = {
    transcript: "Read from the agent's transcript file.",
    otlp: "From the telemetry the agent exports.",
    wrap: "Shiplino ran the agent and watched its process.",
  };
  return { evidence: "observed", detail: via[v.activity_source ?? ""] ?? "Seen as it happened." };
}

/** Commit links: the attribution engine's confidence. */
export function commitEvidence(attribution?: string): EvidenceInfo {
  switch (attribution) {
    case "exact":
      return { evidence: "observed", detail: "The agent ran git commit itself, at the time of the commit." };
    case "likely":
      return {
        evidence: "inferred",
        detail: "The session edited the committed files shortly before; it didn't run the commit.",
      };
    case "shared":
      return { evidence: "inferred", detail: "Several sessions edited the committed files, so they share it." };
    default:
      return { evidence: "unknown", detail: "No attribution recorded." };
  }
}

/** Project assignment: git confirms a repository; otherwise it's grouped by folder. */
export function projectEvidence(kind?: string): EvidenceInfo {
  switch (kind) {
    case "remote":
      return { evidence: "observed", detail: "Git: the working folder is in a repository with this remote." };
    case "git":
      return { evidence: "observed", detail: "Git: the working folder is in this local repository." };
    case "dir":
      return {
        evidence: "inferred",
        detail: "Not a git repository: grouped by the nearest folder with a project file, or the working folder.",
      };
    default:
      return { evidence: "unknown", detail: "The working folder is unknown or the home folder." };
  }
}

/** Subagents: the agent names each one and its parent. */
export function subagentEvidence(id: string, agent?: string): EvidenceInfo {
  if (id.endsWith("/sub:unknown"))
    return { evidence: "unknown", detail: "The agent didn't say which subagent this work belongs to." };
  return {
    evidence: "reported",
    detail: `${agent ? agentName(agent) : "The agent"} names this subagent and the session that started it.`,
  };
}

/** Line counts: the agent's own diff, or counted by Shiplino from the edit. */
export function linesEvidence(source?: string): EvidenceInfo | null {
  switch (source) {
    case "agent":
      return { evidence: "reported", detail: "From the agent's own diff." };
    case "computed":
    case "estimated":
      return {
        evidence: "inferred",
        detail: "Some edits came without a diff from the agent: Shiplino counted their lines from the edit text.",
      };
    default:
      return null;
  }
}

/** Plan usage windows: the agent's own percentages, or tokens Shiplino summed. */
export function limitEvidence(source: "reported" | "estimate"): EvidenceInfo {
  return source === "reported"
    ? { evidence: "reported", detail: "Percentages and reset times the agent reported." }
    : {
        evidence: "inferred",
        detail: "Tokens Shiplino summed: plan quotas aren't published, so there's no percentage.",
      };
}
