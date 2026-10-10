"use client";

import { Bot, ChevronRight, Info, ListTodo, RefreshCw, ShieldCheck, User, Wrench } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { Markdown } from "@/components/session/markdown";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { api, type Conversation, type ConversationMessage } from "@/lib/api";

const PAGE = 200;

function time(ts?: string) {
  if (!ts || ts.startsWith("0001")) return "";
  return new Date(ts).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false });
}

function ToolCall({ m }: { m: ConversationMessage }) {
  return (
    <details className="group rounded-md px-2 py-1 text-xs hover:bg-accent/40">
      <summary className="flex cursor-pointer list-none items-center gap-2 [&::-webkit-details-marker]:hidden">
        <ChevronRight className="size-3.5 shrink-0 text-muted-foreground transition-transform group-open:rotate-90 motion-reduce:transition-none" />
        {m.todos ? (
          <ListTodo className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
        ) : (
          <Wrench className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
        )}
        <span className="shrink-0 font-mono">{m.tool || "tool"}</span>
        {m.text && m.text !== m.tool && (
          <span className="min-w-0 truncate font-mono text-muted-foreground">{m.text}</span>
        )}
        <span className="ml-auto shrink-0 font-mono tabular-nums text-muted-foreground">{time(m.ts)}</span>
      </summary>
      <div className="mt-1 ml-6 flex flex-col gap-1">
        {m.text && <pre className="whitespace-pre-wrap break-all font-mono text-xs">{m.text}</pre>}
        {m.todos && (
          <ul className="flex flex-col gap-0.5">
            {m.todos.map((t, i) => (
              // biome-ignore lint/suspicious/noArrayIndexKey: todo lists have no stable ids
              <li key={i} className="flex gap-2">
                <span className="w-20 shrink-0 font-mono text-muted-foreground">{t.status || "pending"}</span>
                <span>{t.text}</span>
              </li>
            ))}
          </ul>
        )}
      </div>
    </details>
  );
}

function Message({ m }: { m: ConversationMessage }) {
  if (m.role === "tool") return <ToolCall m={m} />;
  const user = m.role === "user";
  const Icon = user ? User : Bot;
  return (
    <div className={`flex gap-3 rounded-md px-2 py-2 text-sm ${user ? "bg-muted/50" : ""}`}>
      <Icon className="mt-0.5 size-4 shrink-0 text-muted-foreground" aria-hidden />
      <div className="flex min-w-0 flex-1 flex-col gap-1">
        <div className="flex items-center gap-2 text-xs text-muted-foreground">
          <span className="font-medium">{user ? "You" : "Agent"}</span>
          <span className="font-mono tabular-nums">{time(m.ts)}</span>
        </div>
        <Markdown text={m.text ?? ""} />
      </div>
    </div>
  );
}

/** ConversationView shows a session's conversation, read on demand from the agent's transcript. */
export function ConversationView({ sessionId }: { sessionId: string }) {
  const [conv, setConv] = useState<Conversation | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  const load = useCallback(
    (offset: number) => {
      setLoading(true);
      api<Conversation>(`/api/v1/sessions/${encodeURIComponent(sessionId)}/conversation?offset=${offset}&limit=${PAGE}`)
        .then((c) => {
          setConv((prev) => (offset > 0 && prev ? { ...c, messages: [...prev.messages, ...c.messages] } : c));
          setError(null);
        })
        .catch((err: Error) => setError(err.message))
        .finally(() => setLoading(false));
    },
    [sessionId],
  );

  useEffect(() => load(0), [load]);

  if (error && !conv)
    return <p className="p-4 text-sm text-muted-foreground">Couldn&apos;t read the conversation: {error}</p>;
  if (!conv) return <Skeleton className="h-64 w-full" />;

  return (
    <Card className="py-2">
      <CardContent className="flex flex-col gap-1 px-2">
        <div className="flex items-start gap-2 px-2 py-1.5 text-xs text-muted-foreground">
          {conv.source === "transcript" ? (
            <ShieldCheck className="mt-0.5 size-3.5 shrink-0" aria-hidden />
          ) : (
            <Info className="mt-0.5 size-3.5 shrink-0 text-status-waiting" aria-hidden />
          )}
          <span className="flex-1">
            {conv.source === "transcript"
              ? "Read just now from the agent's own transcript on this machine. Shiplino doesn't store or sync it; secrets are redacted."
              : conv.note}
            {conv.truncated && " Only the first 20,000 messages are shown."}
          </span>
          <Button size="xs" variant="ghost" onClick={() => load(0)} disabled={loading} aria-label="Reload conversation">
            <RefreshCw className="size-3" /> Reload
          </Button>
        </div>
        {conv.source === "stored" && conv.messages.length > 0 && (
          <Badge variant="outline" className="mx-2 w-fit">
            stored by Shiplino ({conv.capture_level} capture level)
          </Badge>
        )}
        {conv.source !== "none" && conv.messages.length === 0 && (
          <p className="p-2 text-sm text-muted-foreground">No messages yet.</p>
        )}
        {conv.messages.map((m, i) => (
          // biome-ignore lint/suspicious/noArrayIndexKey: messages are an append-only list without ids
          <div key={i} className={m.subagent ? "ml-6 border-l pl-2" : ""}>
            {m.subagent && conv.messages[i - 1]?.subagent !== m.subagent && (
              <Badge variant="outline" className="my-1 font-mono">
                subagent {m.subagent}
              </Badge>
            )}
            <Message m={m} />
          </div>
        ))}
        {conv.next_offset != null && (
          <Button
            size="sm"
            variant="outline"
            className="mx-2 my-1 w-fit"
            disabled={loading}
            onClick={() => load(conv.next_offset ?? 0)}
          >
            Load more ({conv.total - conv.messages.length} left)
          </Button>
        )}
      </CardContent>
    </Card>
  );
}
