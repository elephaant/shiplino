---
name: shiplino-ui
description: Build or change a Shiplino web UI screen (overview, project board, session detail, timeline, insights, search, settings) with shadcn/ui and the default theme. Also covers the first-time setup of web/apps/local. Use for any frontend, layout, component, chart, theme or styling work in web/.
---

# Shiplino UI

Rules: `.claude/rules/web-ui.md`. If `CLAUDE.local.md` exists, follow its extra design guidance too.

## Screen building blocks

| Shiplino screen | Build with |
|-----------------|-----------|
| App shell | shadcn `sidebar`, `breadcrumb`, `dropdown-menu`, mode toggle in the header |
| All projects overview, Insights | `card` stat tiles, Recharts via shadcn `chart`, TanStack Table via `table` |
| Project board | `kanban-board` skill |
| Session detail | `tabs`, `table`, `badge`, `scroll-area`, `sheet` (drawer) |
| Search ⌘K | `command` |
| Settings | `field`, `input`, `switch`, `select` + React Hook Form + Zod |
| Empty states | `empty` |

## First-time setup of `web/apps/local` (one time)

1. `npx create-next-app` (TypeScript, App Router, Tailwind v4, `src/`), then `npx shadcn@latest init` with the `radix-nova` style, then add components with `npx shadcn@latest add …`.
2. Theme: import `@shiplino/ui/src/styles/theme.css` (already written: brand, status and agent tokens, light + dark) in `globals.css` after Tailwind, and map the tokens in `@theme inline`. Add a light/dark/system toggle with `next-themes` (`attribute="class"`). Fonts: Geist Sans + Geist Mono.
3. `next.config.mjs`: `output: "export"`, `images: { unoptimized: true }`, `trailingSlash: true`, plus a dev rewrite of `/api/*` → `http://127.0.0.1:4777/api/*`.
4. Shell per `web-ui.md` "Layout decisions": inset sidebar with project switcher, header with live strip, compact density.
5. Biome for lint/format. Run `npx skills add shadcn/ui` for shadcn guidance.

## Building a screen

1. Route: `src/app/(main)/dashboard/<screen>/page.tsx`. Screen-only parts go in `_components/`.
2. Data: typed fetchers for `/api/v1/*` and a WebSocket subscription (`{op:"sub", topics:[…]}`). No server-side fetching (static export).
3. Use `components/ui/*`. Don't edit them, and wrap at the call site.
4. Colors via tokens only. Agent identity via `--agent-*`. Check both light and dark mode.
5. Verify: `npm run check` and `npm run build` (must produce `out/`), then look at it in a browser in both modes.
