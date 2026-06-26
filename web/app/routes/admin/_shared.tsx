// Admin-surface local helpers (status presentation + live status bridging).
// Surface-local: candidate for hoisting (see sharedGaps).

import { useEffect, useState } from "react";
import { useEventStream } from "~/lib/events/useEventStream";
import { subscribeEvents } from "~/lib/events/eventBus";
import { Badge } from "~/components/ui/badge";
import { cn } from "~/lib/utils";
import type { EventEnvelope, SessionStatus, WASession } from "~/lib/api/types";

/** Map a session status to a shadcn Badge variant + tone class. */
const STATUS_TONE: Record<SessionStatus, string> = {
  working: "border-transparent bg-emerald-500/15 text-emerald-700 dark:text-emerald-400",
  starting: "border-transparent bg-amber-500/15 text-amber-700 dark:text-amber-400",
  scan_qr_code: "border-transparent bg-sky-500/15 text-sky-700 dark:text-sky-400",
  stopped: "border-transparent bg-muted text-muted-foreground",
  logged_out: "border-transparent bg-muted text-muted-foreground",
  failed: "border-transparent bg-destructive/15 text-destructive",
};

const STATUS_LABEL: Record<SessionStatus, string> = {
  working: "working",
  starting: "starting",
  scan_qr_code: "scan QR",
  stopped: "stopped",
  logged_out: "logged out",
  failed: "failed",
};

export function SessionStatusBadge({ status }: { status?: SessionStatus }) {
  if (!status) return <Badge variant="outline">unknown</Badge>;
  return (
    <Badge className={cn("font-medium", STATUS_TONE[status])} aria-label={`status ${status}`}>
      {STATUS_LABEL[status]}
    </Badge>
  );
}

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

/** Format an epoch-millis (or seconds) timestamp into a short local string. */
export function fmtTime(ts?: number | null): string {
  if (!ts) return "—";
  // Backend uses int64 millis; tolerate seconds just in case.
  const ms = ts < 1e12 ? ts * 1000 : ts;
  const d = new Date(ms);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString();
}
