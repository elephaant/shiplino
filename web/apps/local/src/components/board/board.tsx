"use client";

import { move } from "@dnd-kit/helpers";
import { DragDropProvider, DragOverlay, useDroppable } from "@dnd-kit/react";
import { useSortable } from "@dnd-kit/react/sortable";
import { cn } from "cn";
import { useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import { api, type BoardCard, type BoardColumn, type ColumnId } from "@/lib/api";
import { CardView } from "./card";

type Items = Record<ColumnId, BoardCard[]>;

const agentColumns: ColumnId[] = ["running", "waiting"];

function toItems(columns: BoardColumn[]): Items {
  return Object.fromEntries(columns.map((c) => [c.id, c.cards])) as Items;
}

function SortableCard({
  card,
  index,
  column,
  onOpen,
}: {
  card: BoardCard;
  index: number;
  column: ColumnId;
  onOpen: (c: BoardCard) => void;
}) {
  const { ref, isDragging } = useSortable({
    id: card.id,
    index,
    group: column,
    type: "card",
    accept: "card",
    data: { card },
  });
  return (
    <div ref={ref} className={cn(isDragging && "opacity-40")}>
      <CardView card={card} onOpen={() => onOpen(card)} />
    </div>
  );
}

function Column({
  col,
  cards,
  blocked,
  onOpen,
}: {
  col: BoardColumn;
  cards: BoardCard[];
  /** blocked: the card being dragged can't be dropped here. */
  blocked: boolean;
  onOpen: (c: BoardCard) => void;
}) {
  const { ref, isDropTarget } = useDroppable({ id: col.id, type: "column", accept: "card", collisionPriority: 1 });
  if (col.id === "failed" && cards.length === 0) return null; // hidden while empty
  return (
    <section className="flex min-w-56 max-w-sm flex-1 basis-0 flex-col gap-2" aria-label={col.name}>
      <header className="flex items-center justify-between px-1">
        <h2 className="text-sm font-medium">{col.name}</h2>
        <span className="font-mono text-xs tabular-nums text-muted-foreground">{cards.length}</span>
      </header>
      <div
        ref={ref}
        data-blocked={blocked || undefined}
        title={blocked ? "Follows the agent: cards move here by themselves" : undefined}
        className={cn(
          "flex min-h-24 flex-1 flex-col gap-2 rounded-lg bg-muted/60 p-2 transition-colors",
          isDropTarget && !blocked && "bg-accent",
          blocked && "cursor-not-allowed bg-status-failed/10 ring-1 ring-status-failed/40",
        )}
      >
        {cards.map((c, i) => (
          <SortableCard key={c.id} card={c} index={i} column={col.id} onOpen={onOpen} />
        ))}
        {cards.length === 0 && <p className="px-1 py-4 text-center text-xs text-muted-foreground">Nothing here</p>}
      </div>
    </section>
  );
}

/** position picks a fractional position between the card's new neighbors. */
function position(list: BoardCard[], index: number): number {
  const before = list[index - 1]?.position;
  const after = list[index + 1]?.position;
  if (before != null && after != null && before < after) return (before + after) / 2;
  if (before != null) return before + 1024;
  if (after != null) return after - 1024;
  return Date.now();
}

export function Board({
  columns,
  onChanged,
  onOpen,
}: {
  columns: BoardColumn[];
  onChanged: () => void;
  onOpen: (c: BoardCard) => void;
}) {
  const [items, setItems] = useState<Items>(() => toItems(columns));
  const snapshot = useRef<Items>(items);
  const dragging = useRef(false);

  // Server data wins whenever we're not in the middle of a drag.
  useEffect(() => {
    if (!dragging.current) setItems(toItems(columns));
  }, [columns]);

  const columnOf = (state: Items, id: string) =>
    (Object.keys(state) as ColumnId[]).find((k) => state[k].some((c) => c.id === id));

  // refused returns the column a drag would wrongly put an auto card into:
  // Running and Waiting follow the agent.
  const refused = (op: { source: { data?: unknown } | null; target: { id: unknown; type?: unknown } | null }) => {
    const card = (op.source?.data as { card?: BoardCard } | undefined)?.card;
    const target = op.target;
    const targetCol = (target?.type === "column" ? target.id : columnOf(items, String(target?.id))) as
      | ColumnId
      | undefined;
    const no = card?.origin === "auto" && targetCol && agentColumns.includes(targetCol) && targetCol !== card.column;
    return no ? targetCol : null;
  };
  const [blocked, setBlocked] = useState<ColumnId | null>(null);

  return (
    <DragDropProvider
      onDragStart={() => {
        dragging.current = true;
        snapshot.current = items;
      }}
      onDragOver={(event) => {
        const no = refused(event.operation);
        setBlocked(no);
        if (no) {
          event.preventDefault();
          return;
        }
        setItems((cur) => move(cur, event));
      }}
      onDragEnd={async (event) => {
        dragging.current = false;
        setBlocked(null);
        if (event.canceled) {
          setItems(snapshot.current);
          return;
        }
        if (refused(event.operation)) {
          toast.info("Running and Waiting follow the agent", {
            description: "This card moves there by itself when its agent is working or waiting on you.",
          });
        }
        const id = String(event.operation.source?.id);
        const before = columnOf(snapshot.current, id);
        const after = columnOf(items, id);
        if (!after) return;
        const list = items[after];
        const index = list.findIndex((c) => c.id === id);
        const oldIndex = before ? snapshot.current[before].findIndex((c) => c.id === id) : -1;
        if (before === after && index === oldIndex) return;
        try {
          await api(`/api/v1/cards/${encodeURIComponent(id)}`, {
            method: "PATCH",
            body: JSON.stringify({ column: after, position: position(list, index) }),
          });
          onChanged();
        } catch (e) {
          setItems(snapshot.current);
          toast.error((e as Error).message);
        }
      }}
    >
      <div className="flex w-full min-w-0 gap-3 overflow-x-auto pb-4">
        {columns.map((col) => (
          <Column key={col.id} col={col} cards={items[col.id] ?? []} blocked={blocked === col.id} onOpen={onOpen} />
        ))}
      </div>
      <DragOverlay>
        {(source) => {
          const card = (source.data as { card?: BoardCard } | undefined)?.card;
          return card ? <CardView card={card} dragging /> : null;
        }}
      </DragOverlay>
    </DragDropProvider>
  );
}
