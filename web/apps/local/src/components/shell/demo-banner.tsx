"use client";

import { FlaskConical } from "lucide-react";
import { useEffect, useState } from "react";
import { api } from "@/lib/api";

/** A strip above the header while the board is a `shiplino demo` instance (synthetic data). */
export function DemoBanner() {
  const [demo, setDemo] = useState(false);
  useEffect(() => {
    api<{ demo?: boolean }>("/api/v1/status")
      .then((s) => setDemo(s.demo === true))
      .catch(() => {});
  }, []);
  if (!demo) return null;
  return (
    <div
      role="status"
      className="flex items-center justify-center gap-2 border-b bg-primary/10 px-4 py-1.5 text-center text-sm"
    >
      <FlaskConical className="size-4 shrink-0 text-primary" aria-hidden />
      <span>
        <span className="font-medium">Demo data.</span> Synthetic sessions, not your agents. Stop the demo with Ctrl-C;
        its data is deleted.
      </span>
    </div>
  );
}
