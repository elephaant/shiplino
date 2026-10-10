"use client";

import { Copy, Download, Moon, Share2, Sun } from "lucide-react";
import { useTheme } from "next-themes";
import { useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Skeleton } from "@/components/ui/skeleton";
import { api, type Insights } from "@/lib/api";
import { buildWeekCard, type WeekCard } from "@/lib/share-card";
import { renderWeekCard, SHARE_H, SHARE_W, type ShareMode } from "./share-image";

// Days of history used to count the streak.
const STREAK_DAYS = 60;

/** "Share week": a PNG summary of the last 7 days, drawn in the browser. */
export function ShareWeek({ project, apiEquivalent }: { project: string; apiEquivalent: boolean }) {
  const { resolvedTheme } = useTheme();
  const [open, setOpen] = useState(false);
  const [mode, setMode] = useState<ShareMode>("light");
  const [includeProjects, setIncludeProjects] = useState(false);
  const [data, setData] = useState<{ week: Insights; history: Insights["daily"] } | null>(null);
  const [png, setPng] = useState<{ blob: Blob; canvas: HTMLCanvasElement; card: WeekCard } | null>(null);
  const [error, setError] = useState<string | null>(null);
  const preview = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    setData(null);
    setError(null);
    const p = project === "all" ? "" : `&project=${encodeURIComponent(project)}`;
    Promise.all([
      api<Insights>(`/api/v1/insights?days=7${p}`),
      api<Insights>(`/api/v1/insights?days=${STREAK_DAYS}${p}`),
    ])
      .then(([week, history]) => setData({ week, history: history.daily }))
      .catch((e: Error) => setError(e.message));
  }, [open, project]);

  useEffect(() => {
    if (!data) return;
    let live = true;
    const card = buildWeekCard(data.week, data.history, { includeProjects, apiEquivalent });
    renderWeekCard(card, mode)
      .then(({ canvas, blob }) => {
        if (!live) return;
        canvas.className = "size-full";
        canvas.setAttribute("role", "img");
        canvas.setAttribute("aria-label", `Week summary: ${card.sessions} sessions`);
        setPng({ blob, canvas, card });
      })
      .catch((e: Error) => live && setError(e.message));
    return () => {
      live = false;
    };
  }, [data, mode, includeProjects, apiEquivalent]);

  useEffect(() => {
    if (png && preview.current) preview.current.replaceChildren(png.canvas);
  }, [png]);

  const onOpenChange = (o: boolean) => {
    if (o) setMode(resolvedTheme === "dark" ? "dark" : "light");
    else setPng(null);
    setOpen(o);
  };

  const canCopy = typeof window !== "undefined" && "ClipboardItem" in window && !!navigator.clipboard?.write;
  const copy = async () => {
    if (!png) return;
    try {
      await navigator.clipboard.write([new ClipboardItem({ "image/png": png.blob })]);
      toast.success("Image copied");
    } catch (e) {
      toast.error(`Couldn't copy the image: ${(e as Error).message}`);
    }
  };

  const download = () => {
    if (!png) return;
    const url = URL.createObjectURL(png.blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `shiplino-week-${png.card.to}.png`;
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogTrigger asChild>
        <Button variant="outline" size="sm" className="h-8">
          <Share2 className="size-3.5" /> Share week
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>Share your week</DialogTitle>
          <DialogDescription>
            A summary of the last 7 days, drawn on this computer. Nothing is uploaded. It shows totals and agent names
            only; project names, paths, prompts and titles stay out unless you include project names.
          </DialogDescription>
        </DialogHeader>
        <div
          className="relative w-full overflow-hidden rounded-md border bg-muted"
          style={{ aspectRatio: `${SHARE_W} / ${SHARE_H}` }}
        >
          {error ? (
            <p className="p-4 text-destructive text-sm">{error}</p>
          ) : png ? (
            <div ref={preview} className="size-full" />
          ) : (
            <Skeleton className="size-full" />
          )}
        </div>
        <div className="flex flex-wrap items-center justify-between gap-3 text-sm">
          <label className="flex cursor-pointer items-center gap-2">
            <input
              type="checkbox"
              className="size-4 accent-primary"
              checked={includeProjects}
              onChange={(e) => setIncludeProjects(e.target.checked)}
            />
            Include project names
          </label>
          <div className="flex items-center gap-1" role="radiogroup" aria-label="Image theme">
            {(["light", "dark"] as const).map((m) => (
              <Button
                key={m}
                variant={mode === m ? "secondary" : "ghost"}
                size="sm"
                className="h-8"
                role="radio"
                aria-checked={mode === m}
                onClick={() => setMode(m)}
              >
                {m === "light" ? <Sun className="size-3.5" /> : <Moon className="size-3.5" />}
                {m === "light" ? "Light" : "Dark"}
              </Button>
            ))}
          </div>
        </div>
        <DialogFooter>
          <Button
            variant="outline"
            onClick={copy}
            disabled={!png || !canCopy}
            title={canCopy ? undefined : "This browser can't copy images; download the PNG instead."}
          >
            <Copy className="size-4" /> Copy image
          </Button>
          <Button onClick={download} disabled={!png}>
            <Download className="size-4" /> Download PNG
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
