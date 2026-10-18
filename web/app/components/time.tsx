// <Time>: the one way to show a timestamp. Renders a semantic <time> in the
// viewer's local zone, relative by default ("3 min ago") with the full
// absolute timestamp in the hover title. Relative labels re-render every 30s.
//
// SSR note: the server snapshot of the clock differs from the client's, so
// React re-renders once after hydration with the browser's zone and clock
// (useSyncExternalStore), which also fixes a server/client time-zone skew.

import { useSyncExternalStore } from "react";
import {
  formatDateTime,
  formatRelative,
  toDate,
  type DateInput,
} from "~/lib/format";
import { cn } from "~/lib/utils";

const TICK_MS = 30_000;
const listeners = new Set<() => void>();
let timer: ReturnType<typeof setInterval> | null = null;

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  timer ??= setInterval(() => listeners.forEach((l) => l()), TICK_MS);
  return () => {
    listeners.delete(listener);
    if (listeners.size === 0 && timer) {
      clearInterval(timer);
      timer = null;
    }
  };
}

// Rounded to the tick so the snapshot is stable between renders.
const getSnapshot = () => Math.floor(Date.now() / TICK_MS) * TICK_MS;
const getServerSnapshot = () => 0;

/** The current time (ms), refreshed every 30s; 0 during SSR/hydration. */
export function useNow(): number {
  return useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);
}

/** A local-time timestamp: relative label with the absolute time on hover. */
export function Time({
  value,
  format = "relative",
  fallback = "—",
  className,
}: {
  /** Epoch millis/seconds, ISO string or Date. Empty values render `fallback`. */
  value: DateInput;
  /** "relative" ("3 min ago", default) or "absolute" ("Oct 18, 2026, 3:04 PM"). */
  format?: "relative" | "absolute";
  /** Shown when `value` is empty, e.g. "Never". */
  fallback?: string;
  className?: string;
}) {
  const now = useNow();
  const d = toDate(value);
  if (!d) {
    return <span className={cn("text-muted-foreground", className)}>{fallback}</span>;
  }
  const absolute = formatDateTime(d, { seconds: true });
  const label =
    format === "absolute" || now === 0
      ? formatDateTime(d)
      : formatRelative(d);
  return (
    <time
      dateTime={d.toISOString()}
      title={absolute}
      className={cn("whitespace-nowrap tabular-nums", className)}
      suppressHydrationWarning
    >
      {label}
    </time>
  );
}
