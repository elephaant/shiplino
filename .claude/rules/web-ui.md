---
paths:
  - "web/**"
  - "plugins/vscode/**"
---

# Web UI

## Look and theme

- **One theme: the default theme**, defined once in `web/packages/ui/src/styles/theme.css` (shadcn token names, light + `.dark`). Apps import it from `@shiplino/ui` after Tailwind in `globals.css`. No other presets, no preset switcher. Keep the light/dark/system toggle.
  - Brand orange `--primary` = logo color `#DF5E3A`. Neutrals are warm stone (light) and warm charcoal (dark). Radius `0.5rem`.
  - To change a color, edit `theme.css` only and check contrast in both modes.
- Use tokens (`bg-primary`, `text-muted-foreground`, `--chart-1..5`, `--sidebar-*`, `--status-*`, `--agent-*`), never hard-coded colors.
- **Status colors** (`--status-running|waiting|review|done|failed`) and **agent colors** (`--agent-claude|codex|cursor|gemini|copilot|windsurf|other`) are always paired with an icon, never color alone. Use our own agent icons, not vendor logos.

## Layout decisions (Shiplino's own)

- **Sidebar** (shadcn `sidebar`, `inset` variant): a **project switcher** at the top with a live dot + running/waiting count per project, then nav (Overview, Board, Timeline, Office, Insights), with Settings and the daemon status at the bottom.
- **Header:** breadcrumb (project › sprint) · live strip `● 4 running · 1 waiting` (click → "Needs you" queue) · ⌘K search · mode toggle.
- **Density:** compact. Cards `p-3`, body `text-sm`, board columns fixed at ~300px and horizontally scrollable.
- **Data typography:** Geist Sans for UI and **Geist Mono** for commands, paths, ids, costs and durations (`tabular-nums`).
- **Motion:** only for meaning (waiting cards pulse, a card slides when it changes column). Respect `prefers-reduced-motion`.
- **Kanban:** `@dnd-kit/react` board. Use the `kanban-board` skill.

## Stack

Next.js 16 App Router, React 19, TypeScript (strict), Tailwind CSS v4, shadcn/ui (`radix-nova` style), `@dnd-kit/react`, Zustand, TanStack Table, Recharts, lucide-react, sonner, React Hook Form + Zod, Biome. Use npm workspaces (`web/package.json`).

This version of Next.js has breaking changes. Read `node_modules/next/dist/docs/` before relying on memory.

## Local app = static export

`apps/local` is embedded in the Go binary and served by the daemon:
- `output: "export"`, `images.unoptimized: true`, `trailingSlash: true`
- No API routes, server actions, middleware or server-only data fetching
- Data comes from `/api/v1/*` (REST) and `ws://…/api/v1/live` (WebSocket). Auth is a same-origin cookie set by the daemon, so never put the token in JS or localStorage.
- Dynamic views use query params (`/session/?id=…`, `/board/?project=…`)
- Dev: Next dev server proxies `/api` to the daemon on 127.0.0.1:4777

## Structure (co-location)

- Screens: `src/app/(main)/dashboard/<screen>/page.tsx`, with screen-only code in `_components/`
- Shared dashboard pieces (sidebar, header, live strip): `src/app/(main)/dashboard/_components/`
- shadcn primitives in `src/components/ui/` (added with the shadcn CLI): **don't edit them**. Customize at the call site.
- Navigation config: `src/navigation/sidebar-items.ts`. Theme mode: `next-themes` (`class` strategy).
- Code used by more than one app moves to `packages/ui` (`@shiplino/ui`)

## Screens (plan)

All projects overview · Project board (kanban) · Session detail (timeline, files, commands, usage, raw) · Timeline/Gantt · Insights · Office (PixiJS, later) · Search (⌘K via the shadcn `command` component) · Settings.

## Quality

- Live updates are coalesced (≤ 10/s per topic). Virtualize long lists and keep it fast with 1,000+ cards.
- Empty state: "Start any agent. It'll appear here within a second."
- Accessible: keyboard nav (`j/k`, `enter`, `/`, `g b`, `g o`), focus rings, sufficient contrast in both modes.
- Write our own components on top of shadcn/ui. If any third-party file is copied, its license notice goes in `NOTICE` (see [public-repo.md](public-repo.md)).
