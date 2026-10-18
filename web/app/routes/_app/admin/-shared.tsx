// Admin-surface local helpers (status presentation + live status bridging +
// time formatting). Ported verbatim from the v1 admin/_shared.tsx; only imports
// changed to the v2 shared libs. The `-` prefix keeps TanStack Start from
// treating this as a route. All deps (event stream/bus, ui, types) are the
// FROZEN foundation libs — reused, not rewritten.

import { useEffect, useState } from "react";
import { useEventStream } from "~/lib/events/useEventStream";
import { subscribeEvents } from "~/lib/events/eventBus";
import { cn } from "~/lib/utils";
import { formatDateTime } from "~/lib/format";
import type { EventEnvelope, SessionStatus, WASession } from "~/lib/api/types";

/** @deprecated Import `SessionStatusBadge` from `~/components/status-badge` instead. */
export { SessionStatusBadge } from "~/components/status-badge";

/** Stream-connection indicator shared by admin pages. */
export function StreamIndicator() {
  const { status, polling, reconnectNow } = useEventStream();
  const live = status === "open";
  const tone = live
    ? "bg-emerald-500"
    : polling || status === "polling"
      ? "bg-amber-500"
      : status === "reconnecting" || status === "connecting"
        ? "bg-amber-500 animate-pulse"
        : "bg-muted-foreground";
  const label = live
    ? "Live"
    : status === "polling" || polling
      ? "Polling"
      : status === "reconnecting"
        ? "Reconnecting"
        : status === "connecting"
          ? "Connecting"
          : "Offline";
  return (
    <button
      type="button"
      onClick={reconnectNow}
      className="inline-flex items-center gap-1.5 rounded-md border px-2 py-1 text-xs text-muted-foreground hover:bg-accent"
      title="Reconnect the event stream"
    >
      <span className={cn("size-2 rounded-full", tone)} aria-hidden />
      {label}
    </button>
  );
}

/**
 * Overlay live session statuses from the firehose onto an immutable base map.
 * The auth/session events carry a status in their payload; we apply the latest
 * per-session value client-side so the admin table reflects reality without a
 * second socket. Returns a map of sessionId → latest status seen.
 */
export function useLiveSessionStatus(): Record<string, SessionStatus> {
  const [overrides, setOverrides] = useState<Record<string, SessionStatus>>({});
  useEffect(() => {
    return subscribeEvents((e: EventEnvelope) => {
      const status = extractStatus(e);
      if (!status || !e.session) return;
      setOverrides((prev) =>
        prev[e.session] === status ? prev : { ...prev, [e.session]: status },
      );
    });
  }, []);
  return overrides;
}

const SESSION_STATUSES: ReadonlySet<string> = new Set<SessionStatus>([
  "starting",
  "scan_qr_code",
  "working",
  "failed",
  "stopped",
  "logged_out",
]);

/** Best-effort pull of a session status out of an event payload. */
function extractStatus(e: EventEnvelope): SessionStatus | null {
  const p = e.payload as Record<string, unknown> | undefined;
  const candidate = p?.["status"] ?? p?.["state"];
  if (typeof candidate === "string" && SESSION_STATUSES.has(candidate)) {
    return candidate as SessionStatus;
  }
  return null;
}

/** Merge live status overrides into a session row. */
export function withLiveStatus(
  s: WASession,
  overrides: Record<string, SessionStatus>,
): SessionStatus | undefined {
  return overrides[s.id] ?? s.status;
}

/** @deprecated Use `<Time>` (components/time) or `formatDateTime` (lib/format). */
export function fmtTime(ts?: number | null): string {
  return formatDateTime(ts);
}
