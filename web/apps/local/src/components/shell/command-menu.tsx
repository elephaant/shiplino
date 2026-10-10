// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-ALv2

"use client";

import {
  ChartColumn,
  ChartGantt,
  Download,
  FileCode,
  GitCommit,
  LayoutDashboard,
  MessageSquare,
  Search,
  Settings,
  Terminal,
} from "lucide-react";
import { useRouter } from "next/navigation";
import { Fragment, type ReactNode, useEffect, useRef, useState } from "react";
import { AgentDot } from "@/components/common/agent-dot";
import { Button } from "@/components/ui/button";
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
} from "@/components/ui/command";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import { API_BASE, api, type SearchHit } from "@/lib/api";
import { formatAgo } from "@/lib/format";

const groups: { kind: string[]; label: string; icon: typeof Search }[] = [
  { kind: ["turn.start"], label: "Prompts", icon: MessageSquare },
  { kind: ["shell.exec"], label: "Commands", icon: Terminal },
  { kind: ["file.edit"], label: "Files", icon: FileCode },
  { kind: ["git.commit", "git.pr"], label: "Commits", icon: GitCommit },
  { kind: ["session.start", "session.update"], label: "Titles", icon: Search },
];

// highlight renders the server's «match» markers as <mark>.
function highlight(snippet: string): ReactNode {
  return snippet.split(/(«[^»]*»)/).map((part, i) =>
    part.startsWith("«") ? (
      // biome-ignore lint/suspicious/noArrayIndexKey: static split of one string
      <mark key={i} className="rounded-sm bg-primary/20 text-foreground">
        {part.slice(1, -1)}
      </mark>
    ) : (
      // biome-ignore lint/suspicious/noArrayIndexKey: static split of one string
      <Fragment key={i}>{part}</Fragment>
    ),
  );
}

// rootOf maps a subagent session id to its top-level session.
const rootOf = (id: string): string => id.split("/sub:")[0] ?? id;

export function CommandMenu() {
  const router = useRouter();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [hits, setHits] = useState<SearchHit[]>([]);
  const [error, setError] = useState("");
  const seq = useRef(0);
  const [mod, setMod] = useState("Ctrl");

  useEffect(() => {
    if (/Mac|iPhone|iPad/.test(navigator.userAgent)) setMod("⌘");
  }, []);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key.toLowerCase() === "k" && (e.metaKey || e.ctrlKey)) {
        e.preventDefault();
        setOpen((o) => !o);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  useEffect(() => {
    const q = query.trim();
    if (!q) {
      setHits([]);
      setError("");
      return;
    }
    const n = ++seq.current;
    const t = setTimeout(() => {
      api<{ results: SearchHit[] }>(`/api/v1/search?limit=40&q=${encodeURIComponent(q)}`)
        .then((r) => {
          if (n !== seq.current) return; // a newer query is in flight
          setHits(r.results);
          setError("");
        })
        .catch((e: Error) => n === seq.current && setError(e.message));
    }, 150);
    return () => clearTimeout(t);
  }, [query]);

  const go = (href: string) => {
    setOpen(false);
    setQuery("");
    router.push(href);
  };

  return (
    <>
      <Button
        variant="outline"
        size="sm"
        aria-label="Search"
        className="h-8 w-8 justify-center gap-2 text-muted-foreground sm:w-56 sm:justify-start"
        onClick={() => setOpen(true)}
      >
        <Search className="size-3.5" />
        <span className="hidden flex-1 text-left sm:inline">Search…</span>
        <kbd className="hidden rounded border bg-muted px-1.5 font-mono text-[10px] sm:inline">{mod} K</kbd>
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="overflow-hidden p-0 sm:max-w-2xl" showCloseButton={false}>
          <DialogTitle className="sr-only">Search</DialogTitle>
          <DialogDescription className="sr-only">Search prompts, commands, files and commits</DialogDescription>
          {/* Results come from the server, so cmdk's own filtering is off. */}
          <Command shouldFilter={false} className="**:data-[slot=command-input-wrapper]:h-12">
            <CommandInput
              placeholder="Search prompts, commands, files, commits…"
              value={query}
              onValueChange={setQuery}
            />
            <CommandList>
              {query.trim() === "" ? (
                <>
                  <CommandGroup heading="Go to">
                    <CommandItem onSelect={() => go("/")}>
                      <LayoutDashboard /> Overview
                    </CommandItem>
                    <CommandItem onSelect={() => go("/timeline/")}>
                      <ChartGantt /> Timeline
                    </CommandItem>
                    <CommandItem onSelect={() => go("/insights")}>
                      <ChartColumn /> Insights
                    </CommandItem>
                    <CommandItem onSelect={() => go("/settings")}>
                      <Settings /> Settings
                    </CommandItem>
                  </CommandGroup>
                  <CommandSeparator />
                  <CommandGroup heading="Export">
                    <CommandItem
                      onSelect={() => {
                        setOpen(false);
                        window.location.href = `${API_BASE}/api/v1/export?format=csv`;
                      }}
                    >
                      <Download /> All sessions as CSV
                    </CommandItem>
                    <CommandItem
                      onSelect={() => {
                        setOpen(false);
                        window.location.href = `${API_BASE}/api/v1/export?format=json`;
                      }}
                    >
                      <Download /> All sessions as JSON
                    </CommandItem>
                  </CommandGroup>
                </>
              ) : (
                <>
                  <CommandEmpty>{error ? `Search failed: ${error}` : "No matches."}</CommandEmpty>
                  {groups.map((g) => {
                    const items = hits.filter((h) => g.kind.includes(h.kind));
                    if (items.length === 0) return null;
                    return (
                      <CommandGroup key={g.label} heading={g.label}>
                        {items.map((h) => (
                          <CommandItem
                            key={h.event_id}
                            value={h.event_id}
                            onSelect={() => go(`/session?id=${encodeURIComponent(rootOf(h.session_id))}`)}
                            className="flex-col items-start gap-0.5"
                          >
                            <span className="flex w-full items-center gap-2">
                              <g.icon className="size-3.5 shrink-0 text-muted-foreground" />
                              <span className="min-w-0 flex-1 truncate font-mono text-xs">{highlight(h.snippet)}</span>
                              <span className="shrink-0 text-muted-foreground text-xs">{formatAgo(h.ts)}</span>
                            </span>
                            <span className="flex items-center gap-1.5 pl-5.5 text-muted-foreground text-xs">
                              {h.agent && <AgentDot agent={h.agent} />}
                              <span className="truncate">{h.session_title || rootOf(h.session_id)}</span>
                            </span>
                          </CommandItem>
                        ))}
                      </CommandGroup>
                    );
                  })}
                </>
              )}
            </CommandList>
          </Command>
        </DialogContent>
      </Dialog>
    </>
  );
}
