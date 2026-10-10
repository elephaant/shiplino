// The data behind the shareable week card. Pure and dependency-free so it can
// be unit tested with `node --test`. It holds aggregate numbers and agent
// names only: project names are added only when the user opts in, and paths,
// prompts and session titles never get here.
import type { Insights } from "./api";
import { agentName } from "./format.ts";

export interface WeekCardAgent {
  agent: string;
  name: string;
  sessions: number;
  activeMs: number;
}

export interface WeekCard {
  /** First and last day shown, YYYY-MM-DD (local). */
  from: string;
  to: string;
  sessions: number;
  activeMs: number;
  tokens: number;
  costUsd: number;
  /** Some agents run on a flat-rate plan: the cost is what the API would charge. */
  apiEquivalent: boolean;
  prs: number;
  prsMerged: number;
  activeDays: number;
  /** Consecutive days with sessions, ending today (or yesterday if today is quiet). */
  streak: number;
  /** True when the streak reaches the start of the history it was counted from. */
  streakAtLeast: boolean;
  busiestDay?: { date: string; sessions: number };
  topAgents: WeekCardAgent[];
  /** How many projects had work: a number, never names. */
  projectCount: number;
  /** Only present when the user ticked "include project names". */
  projectNames?: string[];
}

export interface WeekCardOptions {
  includeProjects: boolean;
  apiEquivalent: boolean;
}

/**
 * Builds the card from the last 7 days of insights. `history` is a longer
 * daily series (oldest first) used for the streak; without it the streak is
 * counted from the week alone.
 */
export function buildWeekCard(week: Insights, history: Insights["daily"] | undefined, opts: WeekCardOptions): WeekCard {
  const t = week.totals;
  const daily = week.daily;
  let busiest: WeekCard["busiestDay"];
  for (const d of daily) {
    if (d.sessions > 0 && (!busiest || d.sessions > busiest.sessions)) busiest = { date: d.date, sessions: d.sessions };
  }
  const { streak, atLeast } = countStreak(history?.length ? history : daily);
  const projects = week.projects.filter((p) => p.sessions > 0);
  const card: WeekCard = {
    from: daily[0]?.date ?? "",
    to: daily[daily.length - 1]?.date ?? "",
    sessions: t.sessions,
    activeMs: t.active_ms,
    tokens: t.input_tokens + t.output_tokens + t.cache_read_tokens + t.cache_write_tokens,
    costUsd: t.cost_usd,
    apiEquivalent: opts.apiEquivalent,
    prs: t.prs,
    prsMerged: t.prs_merged,
    activeDays: daily.filter((d) => d.sessions > 0).length,
    streak,
    streakAtLeast: atLeast,
    busiestDay: busiest,
    topAgents: [...week.agents]
      .filter((a) => a.sessions > 0)
      .sort((a, b) => b.active_ms - a.active_ms || b.sessions - a.sessions)
      .slice(0, 3)
      .map((a) => ({ agent: a.key, name: agentName(a.key), sessions: a.sessions, activeMs: a.active_ms })),
    projectCount: projects.length,
  };
  if (opts.includeProjects) {
    card.projectNames = projects
      .map((p) => p.name)
      .filter((n): n is string => !!n)
      .slice(0, 3);
  }
  return card;
}

function countStreak(days: Insights["daily"]): { streak: number; atLeast: boolean } {
  let i = days.length - 1;
  if (i >= 0 && days[i]!.sessions === 0) i--; // today isn't over yet
  let n = 0;
  while (i >= 0 && days[i]!.sessions > 0) {
    n++;
    i--;
  }
  return { streak: n, atLeast: n > 0 && i < 0 };
}
