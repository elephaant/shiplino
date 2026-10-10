import type { ReactNode } from "react";

// A small, safe Markdown renderer for transcript text. It builds React
// elements only (never HTML strings), so raw HTML in a message shows as
// text. Supported: fenced code, headings, lists, quotes, paragraphs,
// `code`, **bold** and [links](https://…) (http, https and mailto only).

const inlineRE = /(`+)([\s\S]*?[^`])\1(?!`)|\*\*([^*]+)\*\*|\[([^\]]+)\]\(([^)\s]+)\)/g;
const fenceRE = /^\s*(`{3,}|~{3,})/;
const headingRE = /^#{1,6}\s+(.*)$/;
const listRE = /^\s*(?:[-*+]|\d+[.)])\s+(.*)$/;

function safeHref(href: string): string | null {
  try {
    const u = new URL(href);
    return ["http:", "https:", "mailto:"].includes(u.protocol) ? u.href : null;
  } catch {
    return null;
  }
}

function inline(text: string, key: string): ReactNode[] {
  const out: ReactNode[] = [];
  let last = 0;
  let i = 0;
  for (const m of text.matchAll(inlineRE)) {
    const at = m.index ?? 0;
    if (at > last) out.push(text.slice(last, at));
    const k = `${key}.${i++}`;
    if (m[2] !== undefined) {
      out.push(
        <code key={k} className="rounded bg-muted px-1 py-0.5 font-mono text-xs">
          {m[2]}
        </code>,
      );
    } else if (m[3] !== undefined) {
      out.push(<strong key={k}>{m[3]}</strong>);
    } else {
      const href = safeHref(m[5] ?? "");
      out.push(
        href ? (
          <a key={k} href={href} target="_blank" rel="noopener noreferrer" className="text-primary underline">
            {m[4]}
          </a>
        ) : (
          m[0]
        ),
      );
    }
    last = at + m[0].length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

export function Markdown({ text }: { text: string }) {
  const lines = text.replace(/\r\n/g, "\n").split("\n");
  const line = (j: number) => lines[j] ?? "";
  const blocks: ReactNode[] = [];
  let i = 0;
  while (i < lines.length) {
    const key = `b${i}`;
    const fence = fenceRE.exec(line(i));
    if (fence) {
      const close = fence[1] ?? "```";
      const body: string[] = [];
      i++;
      while (i < lines.length && !line(i).trimStart().startsWith(close)) body.push(line(i++));
      i++;
      blocks.push(
        <pre key={key} className="overflow-x-auto rounded-md bg-muted p-2 font-mono text-xs leading-5">
          <code>{body.join("\n")}</code>
        </pre>,
      );
      continue;
    }
    if (line(i).trim() === "") {
      i++;
      continue;
    }
    const heading = headingRE.exec(line(i));
    if (heading) {
      blocks.push(
        <p key={key} className="font-semibold">
          {inline(heading[1] ?? "", key)}
        </p>,
      );
      i++;
      continue;
    }
    if (listRE.test(line(i))) {
      const ordered = /^\s*\d/.test(line(i));
      const items: ReactNode[] = [];
      while (i < lines.length && listRE.test(line(i))) {
        const k = `${key}.${items.length}`;
        items.push(<li key={k}>{inline(listRE.exec(line(i))?.[1] ?? "", k)}</li>);
        i++;
      }
      blocks.push(
        ordered ? (
          <ol key={key} className="list-decimal pl-5">
            {items}
          </ol>
        ) : (
          <ul key={key} className="list-disc pl-5">
            {items}
          </ul>
        ),
      );
      continue;
    }
    if (line(i).startsWith(">")) {
      const body: string[] = [];
      while (i < lines.length && line(i).startsWith(">")) body.push(line(i++).replace(/^>\s?/, ""));
      blocks.push(
        <blockquote key={key} className="whitespace-pre-wrap border-l-2 pl-3 text-muted-foreground">
          {inline(body.join("\n"), key)}
        </blockquote>,
      );
      continue;
    }
    const para: string[] = [];
    while (
      i < lines.length &&
      line(i).trim() !== "" &&
      !fenceRE.test(line(i)) &&
      !headingRE.test(line(i)) &&
      !listRE.test(line(i)) &&
      !line(i).startsWith(">")
    )
      para.push(line(i++));
    blocks.push(
      <p key={key} className="whitespace-pre-wrap">
        {inline(para.join("\n"), key)}
      </p>,
    );
  }
  return <div className="flex min-w-0 flex-col gap-2 wrap-break-word">{blocks}</div>;
}
