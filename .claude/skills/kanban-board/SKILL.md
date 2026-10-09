---
name: kanban-board
description: Build or change the Shiplino project kanban board (dnd-kit) with live, auto-moving cards with columns Backlog, Running, Waiting on you, Review, Done and Failed, plus nested subagents and pinning. Use for any board, card, column, drag-and-drop or "move card" work.
---

# Shiplino kanban board

Location: `src/app/(main)/dashboard/board/` with `page.tsx` and `_components/`: `kanban.tsx` (board + toolbar + `DragDropProvider`), `kanban-column.tsx`, `sortable-card.tsx`, `card.tsx`, `types.ts`, `store.ts`, `utils.ts`. Use `@dnd-kit/react` (`DragDropProvider`, `DragOverlay`, `isSortable`) and `move` from `@dnd-kit/helpers`. Style with shadcn `card`, `badge`, `button-group`, `input-group`, `tabs` and the default theme tokens. If `CLAUDE.local.md` exists, follow its design guidance.

## Types (`types.ts`)

```ts
export type ColumnId = "backlog" | "running" | "waiting" | "review" | "done" | "failed";
export type AgentName = "claude-code" | "codex" | "cursor" | "gemini-cli" | "copilot-cli" | "windsurf" | "cline" | "opencode" | "aider" | (string & {});

export type SubagentRow = { id: string; type: string; status: "running" | "waiting" | "done" | "failed"; nowDoing?: string; costUsd: number; durationMs: number };

export type Card = {
  id: string; origin: "auto" | "manual"; title: string;
  agent?: AgentName; projectId: string; branch?: string; sprintId?: string;
  nowDoing?: string; startedAt?: string; durationMs: number; costUsd: number;
  costSource?: "computed" | "reported" | "estimated";
  filesChanged: number; linesAdded: number; linesRemoved: number;
  subagents: SubagentRow[]; links: { kind: "pr" | "issue" | "commit"; url: string; state?: string }[];
  pinned: boolean; rolledOver?: boolean;
};
export type BoardState = Record<ColumnId, Card[]>;
```

## Behavior

- **Data is live.** Load from `GET /api/v1/projects/{id}/board` and subscribe to `board:<projectId>`. Apply `task.move` and `session.update` messages in a Zustand store. Keep static data only as a test fixture.
- **Auto cards move themselves.** When the user drags an auto card, call `PATCH /api/v1/tasks/{id}` with `{column, position}`. That **pins** it (`pinned: true`, shown as a pin icon), and auto-moves stop. Provide "Unpin".
- **Forbidden drops:** auto cards can't be dropped into `running` or `waiting`, because those states come from the agent. Show a disabled drop target.
- **Manual cards** (origin `manual`) live in Backlog and can be dragged anywhere. Dropping a running card onto a manual card links them (`POST /tasks/{id}/sessions`).
- **Waiting on you** cards pulse and show the waiting reason ("Approve: `git push`"). The tab title shows `(n) Shiplino`.
- **Failed** column is collapsed when empty.
- **Ordering:** `position` is a REAL (fractional index). Insert between neighbours and never renumber everything.

## Card layout (`card.tsx`, built on shadcn `Card`)

1. Agent icon + `--agent-*` color dot · title (truncate to 2 lines)
2. `project · branch` (muted)
3. Live "now doing" line with an icon by tool kind (✎ edit, 📄 read, ⚙ shell, 🔍 search)
4. Footer: duration · cost (`~$` prefix when estimated) · files `+a −r` · PR badge
5. Subagent rows nested (collapse when > 3: "+ N subagents"), each with a status dot and now-doing. Clicking a waiting subagent jumps to it.
6. Badges: pinned, rolled over, ⚠ conflict ("also edited by …")

## Toolbar

Search, filters (agent, status, model, sprint), view tabs (Board / Timeline / Office), sprint selector, "+ New card" (manual), toggle "Subagents as separate cards".

## Performance

Coalesce WebSocket updates (≤ 10/s), memoize cards by id + version, and virtualize columns past ~100 cards. Test with a 1,000-card fixture.

## Verify

`npm run check && npm run build`, then in a browser: drag pins, forbidden drops are blocked, live updates move unpinned cards, and both light and dark mode look right.
