import { CircleStop, Repeat, ShieldX, SquareTerminal, Wrench, XCircle } from "lucide-react";
import Link from "next/link";
import type { ReactNode } from "react";
import { AgentDot } from "@/components/common/agent-dot";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import type { Failures as FailureData, FailureLinks } from "@/lib/api";
import { agentName, formatAgo } from "@/lib/format";

const sessionHref = (id: string) => `/session/?id=${encodeURIComponent(id)}`;

/** Links to the first sessions of a row, then how many more there are. */
function Sessions({ links, max = 3 }: { links: FailureLinks; max?: number }) {
  const more = links.session_count - Math.min(max, links.sessions.length);
  return (
    <span className="inline-flex flex-wrap items-center justify-end gap-x-2 font-mono text-xs">
      {links.sessions.slice(0, max).map((id) => (
        <Link key={id} href={sessionHref(id)} className="text-primary hover:underline" title={id}>
          {shortId(id)}
        </Link>
      ))}
      {more > 0 && <span className="text-muted-foreground">+{more}</span>}
    </span>
  );
}

/** The last characters of a session id, enough to tell sessions apart. */
function shortId(id: string): string {
  const tail = id.slice(id.lastIndexOf(":") + 1);
  return tail.length > 8 ? tail.slice(0, 8) : tail;
}

function Count({ icon, label, value }: { icon: ReactNode; label: string; value: number }) {
  return (
    <div className="flex items-center gap-2.5 px-4 py-3">
      <span className={value > 0 ? "text-status-failed" : "text-muted-foreground"} aria-hidden>
        {icon}
      </span>
      <div className="min-w-0">
        <div className="font-mono text-lg leading-none tabular-nums">{value.toLocaleString()}</div>
        <div className="truncate text-muted-foreground text-xs">{label}</div>
      </div>
    </div>
  );
}

function Section({ title, description, children }: { title: string; description: string; children: ReactNode }) {
  return (
    <Card className="h-full gap-3">
      <CardHeader>
        <CardTitle>{title}</CardTitle>
        <CardDescription>{description}</CardDescription>
      </CardHeader>
      <CardContent>{children}</CardContent>
    </Card>
  );
}

function None({ children }: { children: ReactNode }) {
  return <p className="text-muted-foreground text-sm">{children}</p>;
}

export function Failures({ f, projectNames }: { f: FailureData; projectNames: Record<string, string> }) {
  const project = (id: string) => (id ? projectNames[id] || id : "Unsorted");
  return (
    <div className="flex flex-col gap-4">
      <div>
        <h2 className="font-semibold text-lg tracking-tight">Failures</h2>
        <p className="text-muted-foreground text-sm">
          What went wrong, grouped by tool, program and exit code. Click a session to see what happened.
        </p>
      </div>
      <div className="overflow-hidden rounded-xl border bg-card">
        <div className="grid grid-cols-2 divide-x divide-y sm:grid-cols-3 lg:grid-cols-5 lg:divide-y-0">
          <Count icon={<Wrench className="size-4" />} label="failed tool calls" value={f.tool_failures} />
          <Count
            icon={<SquareTerminal className="size-4" />}
            label="commands exited non-zero"
            value={f.shell_failures}
          />
          <Count icon={<ShieldX className="size-4" />} label="denied or refused" value={f.denials} />
          <Count icon={<Repeat className="size-4" />} label="retry loops" value={f.retry_loops} />
          <Count icon={<XCircle className="size-4" />} label="sessions ended badly" value={f.ended_badly} />
        </div>
      </div>

      <div className="grid grid-cols-1 items-stretch gap-4 xl:grid-cols-2">
        <Section title="Failing tools" description="Tool calls that failed or weren't allowed, per agent">
          {f.tools.length === 0 ? (
            <None>No failed tool calls in this period.</None>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Tool</TableHead>
                  <TableHead className="text-right">Failed</TableHead>
                  <TableHead className="text-right">Denied</TableHead>
                  <TableHead className="text-right">Sessions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {f.tools.map((r) => (
                  <TableRow key={`${r.agent}/${r.tool}/${r.tool_raw}`}>
                    <TableCell className="max-w-56">
                      <div className="flex items-center gap-2">
                        <AgentDot agent={r.agent} />
                        <span className="truncate font-mono text-xs" title={`${agentName(r.agent)} · ${r.tool_raw}`}>
                          {r.tool_raw}
                        </span>
                      </div>
                    </TableCell>
                    <TableCell className="text-right font-mono tabular-nums">{r.failures || "–"}</TableCell>
                    <TableCell className="text-right font-mono tabular-nums">{r.denials || "–"}</TableCell>
                    <TableCell className="text-right">
                      <Sessions links={r} />
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </Section>

        <Section title="Failing commands" description="Shell commands by program and exit code">
          {f.shell.length === 0 ? (
            <None>No command exited with an error in this period.</None>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Program</TableHead>
                  <TableHead className="text-right">Exit</TableHead>
                  <TableHead className="text-right">Times</TableHead>
                  <TableHead className="text-right">Sessions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {f.shell.map((r) => (
                  <TableRow key={`${r.program}/${r.exit_code}`}>
                    <TableCell className="max-w-56">
                      <div className="flex items-center gap-2">
                        <span className="flex gap-0.5">
                          {r.agents.map((a) => (
                            <AgentDot key={a} agent={a} />
                          ))}
                        </span>
                        {r.program ? (
                          <span className="truncate font-mono text-xs">{r.program}</span>
                        ) : (
                          <span className="text-muted-foreground text-xs">unknown program</span>
                        )}
                      </div>
                    </TableCell>
                    <TableCell className="text-right font-mono tabular-nums">{r.exit_code}</TableCell>
                    <TableCell className="text-right font-mono tabular-nums">{r.failures}</TableCell>
                    <TableCell className="text-right">
                      <Sessions links={r} />
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </Section>

        <Section
          title="Retry loops"
          description="The same tool failing on the same file or program 3+ times with no success in between"
        >
          {f.loops.length === 0 ? (
            <None>No retry loops in this period.</None>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Target</TableHead>
                  <TableHead className="text-right">Failures</TableHead>
                  <TableHead className="text-right">Last</TableHead>
                  <TableHead className="text-right">Session</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {f.loops.map((l) => (
                  <TableRow key={`${l.session}/${l.tool_raw}/${l.target}/${l.first}`}>
                    <TableCell className="max-w-64">
                      <div className="flex items-center gap-2">
                        <AgentDot agent={l.agent} />
                        <span className="shrink-0 font-mono text-muted-foreground text-xs">{l.tool_raw}</span>
                        <span className="truncate font-mono text-xs" title={l.target}>
                          {l.target}
                        </span>
                      </div>
                    </TableCell>
                    <TableCell className="text-right font-mono tabular-nums">{l.failures}×</TableCell>
                    <TableCell className="text-right text-muted-foreground text-xs">{formatAgo(l.last)}</TableCell>
                    <TableCell className="text-right">
                      <Sessions links={{ session_count: 1, sessions: [l.session] }} />
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </Section>

        <Section title="Ended badly" description="Sessions whose last turn failed or was interrupted">
          {f.endings.length === 0 ? (
            <None>Every session in this period ended cleanly.</None>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Session</TableHead>
                  <TableHead>Project</TableHead>
                  <TableHead>How</TableHead>
                  <TableHead className="text-right">When</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {f.endings.map((e) => (
                  <TableRow key={e.session}>
                    <TableCell>
                      <div className="flex items-center gap-2">
                        <AgentDot agent={e.agent} />
                        <Link href={sessionHref(e.session)} className="font-mono text-primary text-xs hover:underline">
                          {shortId(e.session)}
                        </Link>
                      </div>
                    </TableCell>
                    <TableCell className="max-w-40 truncate text-sm">{project(e.project)}</TableCell>
                    <TableCell>
                      {e.status === "error" ? (
                        <span className="inline-flex items-center gap-1 text-status-failed text-xs">
                          <XCircle className="size-3.5" aria-hidden /> error
                        </span>
                      ) : (
                        <span className="inline-flex items-center gap-1 text-xs">
                          <CircleStop className="size-3.5 text-status-waiting" aria-hidden /> interrupted
                        </span>
                      )}
                    </TableCell>
                    <TableCell className="text-right text-muted-foreground text-xs">{formatAgo(e.at)}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </Section>
      </div>

      {f.projects.length > 0 && (
        <Section title="Failures by project" description="Counts per project; sessions link to the latest ones">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Project</TableHead>
                <TableHead className="text-right">Tools</TableHead>
                <TableHead className="text-right">Commands</TableHead>
                <TableHead className="text-right">Denied</TableHead>
                <TableHead className="text-right">Loops</TableHead>
                <TableHead className="text-right">Ended badly</TableHead>
                <TableHead className="text-right">Sessions</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {f.projects.map((p) => (
                <TableRow key={p.key || "unsorted"}>
                  <TableCell className="max-w-48 truncate font-medium">{p.name || project(p.key)}</TableCell>
                  <TableCell className="text-right font-mono tabular-nums">{p.tool_failures}</TableCell>
                  <TableCell className="text-right font-mono tabular-nums">{p.shell_failures}</TableCell>
                  <TableCell className="text-right font-mono tabular-nums">{p.denials}</TableCell>
                  <TableCell className="text-right font-mono tabular-nums">{p.retry_loops}</TableCell>
                  <TableCell className="text-right font-mono tabular-nums">{p.ended_badly}</TableCell>
                  <TableCell className="text-right">
                    <Sessions links={p} />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Section>
      )}
    </div>
  );
}
