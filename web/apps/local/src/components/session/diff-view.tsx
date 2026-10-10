// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

"use client";

import { cn } from "cn";
import { ChevronRight, FileMinus, FilePen, FilePlus, Info, ShieldAlert } from "lucide-react";
import Link from "next/link";
import { useEffect, useState } from "react";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { type AgentEvent, api, type FileSummary } from "@/lib/api";

const str = (d: Record<string, unknown> | undefined, k: string) => (typeof d?.[k] === "string" ? (d[k] as string) : "");
const num = (d: Record<string, unknown> | undefined, k: string) => (typeof d?.[k] === "number" ? (d[k] as number) : 0);

const opIcon = { create: FilePlus, delete: FileMinus } as Record<string, typeof FilePen>;

interface Line {
  kind: "hunk" | "add" | "del" | "ctx";
  text: string;
  old?: number;
  new?: number;
}

/** parsePatch reads unified-diff hunks; line numbers only when the hunk header has them. */
export function parsePatch(patch: string): Line[] {
  const out: Line[] = [];
  let o = 0;
  let n = 0;
  const lines = patch.split("\n");
  if (lines[lines.length - 1] === "") lines.pop();
  for (const l of lines) {
    if (l.startsWith("@@")) {
      const m = /^@@ -(\d+)(?:,\d+)? \+(\d+)/.exec(l);
      o = m ? Number(m[1]) : 0;
      n = m ? Number(m[2]) : 0;
      // A start of 0 (a new file, or a hunk without numbers) shows no numbers.
      out.push({ kind: "hunk", text: l });
      continue;
    }
    if (l.startsWith("---") || l.startsWith("+++")) continue; // file headers
    const kind = l.startsWith("+") ? "add" : l.startsWith("-") ? "del" : "ctx";
    const line: Line = { kind, text: l.slice(1) };
    if (kind !== "add" && o) line.old = o++;
    if (kind !== "del" && n) line.new = n++;
    out.push(line);
  }
  return out;
}

const rowTone: Record<Line["kind"], string> = {
  hunk: "bg-muted/60 text-muted-foreground",
  add: "bg-status-done/12",
  del: "bg-status-failed/12",
  ctx: "",
};
const sign: Record<Line["kind"], string> = { hunk: "", add: "+", del: "−", ctx: " " };

function Patch({ patch }: { patch: string }) {
  const lines = parsePatch(patch);
  return (
    <div className="overflow-x-auto rounded-md border">
      <table className="w-full border-collapse font-mono text-xs leading-5">
        <tbody>
          {lines.map((l, i) => (
            // biome-ignore lint/suspicious/noArrayIndexKey: lines have no identity beyond their position
            <tr key={i} className={rowTone[l.kind]}>
              {l.kind === "hunk" ? (
                <td colSpan={4} className="px-2 py-0.5">
                  {l.text}
                </td>
              ) : (
                <>
                  <td className="w-10 select-none px-2 text-right text-muted-foreground tabular-nums">{l.old ?? ""}</td>
                  <td className="w-10 select-none px-2 text-right text-muted-foreground tabular-nums">{l.new ?? ""}</td>
                  <td
                    className={cn(
                      "w-4 select-none text-center",
                      l.kind === "add" && "text-status-done",
                      l.kind === "del" && "text-status-failed",
                    )}
                    aria-label={l.kind === "add" ? "added" : l.kind === "del" ? "removed" : undefined}
                  >
                    {sign[l.kind]}
                  </td>
                  <td className="whitespace-pre pr-4">{l.text}</td>
                </>
              )}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function Edits({
  sessionId,
  file,
  actorLabel,
}: {
  sessionId: string;
  file: FileSummary;
  actorLabel: (id: string) => string;
}) {
  const [edits, setEdits] = useState<AgentEvent[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  // Refetch when the file gets another edit (the summary changes).
  // biome-ignore lint/correctness/useExhaustiveDependencies: edits and last_at signal new edits
  useEffect(() => {
    api<{ edits: AgentEvent[] }>(
      `/api/v1/sessions/${encodeURIComponent(sessionId)}/files?path=${encodeURIComponent(file.path)}`,
    )
      .then((r) => setEdits(r.edits))
      .catch((e: Error) => setError(e.message));
  }, [sessionId, file.path, file.edits, file.last_at]);

  if (error) return <p className="px-3 pb-3 text-sm text-status-failed">{error}</p>;
  if (!edits) return <Skeleton className="mx-3 mb-3 h-24" />;
  return (
    <div className="flex flex-col gap-3 px-3 pb-3">
      {edits.map((e, i) => {
        const d = e.data;
        const patch = str(d, "patch");
        const actor = e.actor_id && e.actor_id !== e.session_id ? actorLabel(e.actor_id) : "";
        return (
          <div key={e.id} className="flex flex-col gap-1.5">
            <p className="flex flex-wrap items-center gap-2 text-muted-foreground text-xs">
              <span className="font-medium text-foreground">Edit {i + 1}</span>
              <span className="font-mono tabular-nums">{new Date(e.ts).toLocaleTimeString([], { hour12: false })}</span>
              <span className="font-mono tabular-nums">
                <span className="text-status-done">+{num(d, "lines_added")}</span>{" "}
                <span className="text-status-failed">−{num(d, "lines_removed")}</span>
              </span>
              {actor && <Badge variant="outline">{actor}</Badge>}
              {patch && (
                <span>{str(d, "patch_source") === "agent" ? "the agent's own diff" : "diff built from the edit"}</span>
              )}
            </p>
            {patch ? (
              <Patch patch={patch} />
            ) : (
              <p className="flex items-center gap-1.5 rounded-md border border-dashed px-3 py-2 text-muted-foreground text-xs">
                {str(d, "patch_omitted") === "secret_file" ? (
                  <>
                    <ShieldAlert className="size-3.5" aria-hidden /> Secret file: Shiplino never stores its contents.
                  </>
                ) : str(d, "patch_omitted") === "capture_level" ? (
                  <>
                    <Info className="size-3.5" aria-hidden /> Recorded below capture level full: line counts only.
                  </>
                ) : (
                  <>
                    <Info className="size-3.5" aria-hidden /> No diff stored for this edit, only its line counts.
                  </>
                )}
              </p>
            )}
            {d?.patch_truncated === true && (
              <p className="text-muted-foreground text-xs">Diff cut at 64 KB; the rest wasn&apos;t stored.</p>
            )}
          </div>
        );
      })}
    </div>
  );
}

/** DiffView lists the files a session changed, with each edit's stored diff on demand. */
export function DiffView({
  sessionId,
  version,
  rel,
  actorLabel,
}: {
  sessionId: string;
  version: number;
  rel: (p: string) => string;
  actorLabel: (id: string) => string;
}) {
  const [files, setFiles] = useState<FileSummary[] | null>(null);
  const [level, setLevel] = useState<string>("");
  const [open, setOpen] = useState<Set<string>>(new Set());

  // biome-ignore lint/correctness/useExhaustiveDependencies: refetch on live changes
  useEffect(() => {
    api<{ files: FileSummary[] }>(`/api/v1/sessions/${encodeURIComponent(sessionId)}/files`)
      .then((r) => setFiles(r.files))
      .catch(() => setFiles([]));
  }, [sessionId, version]);
  useEffect(() => {
    api<{ capture_level: string }>("/api/v1/settings")
      .then((s) => setLevel(s.capture_level))
      .catch(() => {});
  }, []);

  if (!files) return <Skeleton className="h-40 w-full" />;
  const toggle = (p: string) =>
    setOpen((s) => {
      const n = new Set(s);
      if (n.has(p)) n.delete(p);
      else n.add(p);
      return n;
    });
  const anyPatch = files.some((f) => f.patches > 0);
  const added = files.reduce((n, f) => n + f.lines_added, 0);
  const removed = files.reduce((n, f) => n + f.lines_removed, 0);

  return (
    <Card className="gap-0 py-0">
      <CardContent className="flex flex-col px-0">
        {files.length > 0 && (
          <p className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b px-4 py-2.5 text-muted-foreground text-xs">
            <span>
              {files.length} {files.length === 1 ? "file" : "files"} changed
            </span>
            <span className="font-mono tabular-nums">
              <span className="text-status-done">+{added}</span> <span className="text-status-failed">−{removed}</span>
            </span>
            {level && level !== "full" ? (
              <span className="flex basis-full items-start gap-1.5 sm:basis-auto">
                <Info className="mt-px size-3.5 shrink-0" aria-hidden />
                <span>
                  Diffs are stored only at capture level full. Change <code className="font-mono">capture_level</code>{" "}
                  in <code className="font-mono">~/.shiplino/config.toml</code>.{" "}
                  <Link href="/settings/#privacy" className="text-foreground underline underline-offset-2">
                    Settings
                  </Link>
                </span>
              </span>
            ) : (
              !anyPatch && (
                <span className="flex items-center gap-1.5">
                  <Info className="size-3.5" aria-hidden /> No diffs were stored for these edits, only line counts.
                </span>
              )
            )}
          </p>
        )}
        {files.length === 0 && <p className="px-4 py-3 text-muted-foreground text-sm">No files changed.</p>}
        {files.map((f) => {
          const Icon = opIcon[f.op] ?? FilePen;
          const expanded = open.has(f.path);
          const total = f.lines_added + f.lines_removed;
          return (
            <div key={f.path} className="border-b last:border-b-0">
              <button
                type="button"
                className="flex w-full items-center gap-2 px-3 py-2 text-left text-sm hover:bg-accent/40 focus-visible:bg-accent/40 focus-visible:outline-2 focus-visible:outline-ring focus-visible:-outline-offset-2"
                aria-expanded={expanded}
                onClick={() => toggle(f.path)}
              >
                <ChevronRight
                  className={cn("size-4 shrink-0 text-muted-foreground transition-transform", expanded && "rotate-90")}
                  aria-hidden
                />
                <Icon className="size-4 shrink-0 text-muted-foreground" aria-label={f.op} />
                <span className="min-w-0 flex-1 truncate font-mono text-xs" title={f.path}>
                  {rel(f.path)}
                </span>
                {f.edits > 1 && <span className="text-muted-foreground text-xs">{f.edits} edits</span>}
                <span className="w-20 text-right font-mono text-xs tabular-nums">
                  <span className="text-status-done">+{f.lines_added}</span>{" "}
                  <span className="text-status-failed">−{f.lines_removed}</span>
                </span>
                <span className="hidden h-1.5 w-16 overflow-hidden rounded-full bg-muted sm:flex" aria-hidden>
                  {total > 0 && (
                    <>
                      <span className="bg-status-done" style={{ width: `${(f.lines_added / total) * 100}%` }} />
                      <span className="bg-status-failed" style={{ width: `${(f.lines_removed / total) * 100}%` }} />
                    </>
                  )}
                </span>
              </button>
              {expanded && <Edits sessionId={sessionId} file={f} actorLabel={actorLabel} />}
            </div>
          );
        })}
      </CardContent>
    </Card>
  );
}
