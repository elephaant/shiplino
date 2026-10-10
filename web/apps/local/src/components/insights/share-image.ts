// Draws the week card onto a 1200×630 canvas (the usual social preview size)
// with the default theme's tokens. Everything happens in the browser: no
// server, no upload.
import { agentVar, formatCost, formatDuration, formatTokens } from "@/lib/format";
import type { WeekCard } from "@/lib/share-card";

export const SHARE_W = 1200;
export const SHARE_H = 630;

export type ShareMode = "light" | "dark";

/** Theme custom properties for a mode, read from the loaded stylesheets. */
function themeVars(mode: ShareMode): Map<string, string> {
  const light = new Map<string, string>();
  const dark = new Map<string, string>();
  const has = (sel: string, s: string) => sel.split(",").some((p) => p.trim() === s);
  const visit = (rules: CSSRuleList) => {
    for (const r of Array.from(rules)) {
      if (r instanceof CSSStyleRule) {
        const into = has(r.selectorText, ":root") ? light : has(r.selectorText, ".dark") ? dark : undefined;
        if (into) {
          for (const name of Array.from(r.style)) {
            if (name.startsWith("--")) into.set(name, r.style.getPropertyValue(name).trim());
          }
        }
      }
      if ("cssRules" in r) visit((r as CSSGroupingRule).cssRules);
    }
  };
  for (const sheet of Array.from(document.styleSheets)) {
    try {
      visit(sheet.cssRules);
    } catch {
      // A cross-origin sheet can't be read; ours are same-origin.
    }
  }
  return mode === "dark" ? new Map([...light, ...dark]) : light;
}

function loadImage(src: string): Promise<HTMLImageElement> {
  return new Promise((resolve, reject) => {
    const img = new Image();
    img.onload = () => resolve(img);
    img.onerror = () => reject(new Error(`can't load ${src}`));
    img.src = src;
  });
}

function shortDate(iso: string, opts: Intl.DateTimeFormatOptions): string {
  const [y, m, d] = iso.split("-").map(Number);
  if (!y || !m || !d) return "";
  return new Date(y, m - 1, d).toLocaleDateString("en-US", opts);
}

function hours(ms: number): string {
  const h = ms / 3_600_000;
  if (h < 1) return formatDuration(ms);
  return `${h < 10 ? h.toFixed(1) : Math.round(h)}h`;
}

function fit(ctx: CanvasRenderingContext2D, text: string, max: number): string {
  if (ctx.measureText(text).width <= max) return text;
  let t = text;
  while (t.length > 1 && ctx.measureText(`${t}…`).width > max) t = t.slice(0, -1);
  return `${t}…`;
}

function roundRect(ctx: CanvasRenderingContext2D, x: number, y: number, w: number, h: number, r: number) {
  ctx.beginPath();
  ctx.roundRect(x, y, w, h, r);
}

/**
 * Renders the card. The canvas is the preview (the page's CSP blocks blob:
 * images) and the blob is the PNG to download or copy.
 */
export async function renderWeekCard(
  card: WeekCard,
  mode: ShareMode,
): Promise<{ canvas: HTMLCanvasElement; blob: Blob }> {
  const vars = themeVars(mode);
  const root = getComputedStyle(document.documentElement);
  // Read a token for the chosen mode; fall back to the page's current value.
  const v = (name: string) => vars.get(name) || root.getPropertyValue(name).trim();
  const sans = root.getPropertyValue("--font-geist-sans").trim() || "ui-sans-serif, system-ui, sans-serif";
  const mono = root.getPropertyValue("--font-geist-mono").trim() || "ui-monospace, monospace";
  await document.fonts.ready;
  const logo = await loadImage("/logo.svg");

  const canvas = document.createElement("canvas");
  canvas.width = SHARE_W;
  canvas.height = SHARE_H;
  const ctx = canvas.getContext("2d");
  if (!ctx) throw new Error("This browser can't draw images (no canvas 2D).");

  const fg = v("--foreground");
  const muted = v("--muted-foreground");
  const primary = v("--primary");
  const font = (size: number, weight = 400, family = sans) => {
    ctx.font = `${weight} ${size}px ${family}`;
  };
  const text = (s: string, x: number, y: number, color: string, align: CanvasTextAlign = "left") => {
    ctx.fillStyle = color;
    ctx.textAlign = align;
    ctx.fillText(s, x, y);
  };

  // Background and panel.
  ctx.fillStyle = v("--background");
  ctx.fillRect(0, 0, SHARE_W, SHARE_H);
  roundRect(ctx, 32, 32, SHARE_W - 64, SHARE_H - 64, 20);
  ctx.fillStyle = v("--card");
  ctx.fill();
  ctx.strokeStyle = v("--border");
  ctx.lineWidth = 2;
  ctx.stroke();
  ctx.fillStyle = primary;
  ctx.fillRect(32 + 20, 32, SHARE_W - 64 - 40, 5);

  const L = 80;
  const R = SHARE_W - 80;

  // Header: logo, wordmark, week.
  ctx.drawImage(logo, L - 4, 70, 48, 48);
  font(32, 600);
  text("Shiplino", L + 54, 106, fg);
  font(20);
  const range = `${shortDate(card.from, { month: "short", day: "numeric" })} – ${shortDate(card.to, { month: "short", day: "numeric", year: "numeric" })}`;
  text(range, R, 104, muted, "right");

  font(40, 600);
  text("My week with AI coding agents", L, 182, fg);

  // Stat tiles.
  const tiles: [string, string][] = [
    ["Sessions", card.sessions.toLocaleString("en-US")],
    ["Agent-hours", hours(card.activeMs)],
    ["Tokens", formatTokens(card.tokens)],
    [card.apiEquivalent ? "API-equivalent cost" : "Cost at list prices", formatCost(card.costUsd)],
  ];
  const gap = 16;
  const tw = (R - L - gap * 3) / 4;
  tiles.forEach(([label, value], i) => {
    const x = L + i * (tw + gap);
    roundRect(ctx, x, 214, tw, 124, 12);
    ctx.fillStyle = v("--background");
    ctx.fill();
    font(18);
    text(label, x + 20, 250, muted);
    font(44, 600, mono);
    text(fit(ctx, value, tw - 40), x + 20, 312, i === 0 ? primary : fg);
  });

  // Top agents (left) and highlights (right).
  const mid = L + (R - L) / 2;
  font(18);
  text("Top agents", L, 392, muted);
  text("Highlights", mid + 24, 392, muted);

  const maxMs = Math.max(1, ...card.topAgents.map((a) => a.activeMs));
  if (!card.topAgents.length) {
    font(20);
    text("No agent sessions this week", L, 432, muted);
  }
  card.topAgents.forEach((a, i) => {
    const y = 432 + i * 42;
    const color = vars.get(agentVar(a.agent).slice(4, -1)) || v("--agent-other");
    ctx.fillStyle = color;
    ctx.beginPath();
    ctx.arc(L + 7, y - 7, 7, 0, Math.PI * 2);
    ctx.fill();
    font(22, 500);
    text(fit(ctx, a.name, 150), L + 24, y, fg);
    const bx = L + 190;
    const bw = mid - 120 - bx;
    roundRect(ctx, bx, y - 13, bw, 12, 6);
    ctx.fillStyle = v("--border");
    ctx.fill();
    roundRect(ctx, bx, y - 13, Math.max(12, (bw * a.activeMs) / maxMs), 12, 6);
    ctx.fillStyle = color;
    ctx.fill();
    font(20, 400, mono);
    text(hours(a.activeMs), mid - 24, y, fg, "right");
  });

  const prs: [string, string] =
    card.prsMerged > 0 || card.prs === 0 ? ["PRs merged", String(card.prsMerged)] : ["Pull requests", String(card.prs)];
  const busiest = card.busiestDay
    ? `${shortDate(card.busiestDay.date, { weekday: "long" })} · ${card.busiestDay.sessions} session${card.busiestDay.sessions === 1 ? "" : "s"}`
    : "—";
  const streak = card.streak
    ? `${card.streak}${card.streakAtLeast ? "+" : ""} day${card.streak === 1 ? "" : "s"}`
    : "—";
  const projects = card.projectNames?.length ? card.projectNames.join(", ") : String(card.projectCount);
  const rows: [string, string][] = [
    prs,
    ["Streak", streak],
    ["Busiest day", busiest],
    [card.projectNames?.length ? "Projects" : "Projects worked on", projects],
  ];
  rows.forEach(([label, value], i) => {
    const y = 432 + i * 34;
    font(20);
    text(label, mid + 24, y, muted);
    font(20, 500, mono);
    text(fit(ctx, value, R - mid - 220), R, y, fg, "right");
  });

  // Footer.
  font(16);
  text("Recorded locally with Shiplino", L, 568, muted);

  const blob = await new Promise<Blob>((resolve, reject) =>
    canvas.toBlob((b) => (b ? resolve(b) : reject(new Error("Couldn't create the PNG."))), "image/png"),
  );
  return { canvas, blob };
}
