// Admin-surface local helpers: the live-stream chip, tone badges, org-name
// lookup and live status bridging. The `-` prefix keeps TanStack Start from
// treating this as a route. All deps (event stream/bus, ui, types) are the
// FROZEN foundation libs — reused, not rewritten.

import { useEffect, useMemo, useState } from "react";
import { useEventStream } from "~/lib/events/useEventStream";
import { subscribeEvents } from "~/lib/events/eventBus";
import { cn } from "~/lib/utils";
import { middleTruncate } from "~/lib/format";
import { Badge } from "~/components/ui/badge";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "~/components/ui/tooltip";
import { useOrgs } from "./-admin-client";
import type { StatusMeta, Tone } from "./-gateway-reconciliation";
import type { EventEnvelope, SessionStatus, WASession } from "~/lib/api/types";

/**
 * Stream-connection chip shared by admin pages. It reads as a status while the
 * stream is live; when it isn't, it becomes a button that reconnects now.
 */
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
  const chip = "inline-flex h-7 items-center gap-1.5 rounded-full border px-2.5 text-xs font-medium text-muted-foreground";
  const dot = <span className={cn("size-2 rounded-full", tone)} aria-hidden />;
  return (
    <TooltipProvider delayDuration={300}>
      <Tooltip>
        <TooltipTrigger asChild>
          {live ? (
            <span className={chip} tabIndex={0} aria-label="Live updates: connected">
              {dot}
              {label}
            </span>
          ) : (
            <button
              type="button"
              onClick={reconnectNow}
              className={cn(chip, "transition-colors duration-150 ease-soft hover:bg-muted hover:text-foreground")}
            >
              {dot}
              {label}
            </button>
          )}
        </TooltipTrigger>
        <TooltipContent>
          {live ? "Live updates are streaming." : "Live updates are not connected. Click to reconnect."}
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
}

/** Tinted pill classes per tone, matching <SessionStatusBadge>. */
export const TONE_CLASS: Record<Tone, { badge: string; dot: string }> = {
  ok: { badge: "bg-emerald-500/12 text-emerald-700 dark:text-emerald-400", dot: "bg-emerald-500" },
  warn: { badge: "bg-amber-500/12 text-amber-700 dark:text-amber-400", dot: "bg-amber-500" },
  info: { badge: "bg-sky-500/12 text-sky-700 dark:text-sky-400", dot: "bg-sky-500" },
  muted: { badge: "bg-muted text-muted-foreground", dot: "bg-muted-foreground/60" },
  bad: { badge: "bg-destructive/10 text-destructive dark:bg-destructive/20", dot: "bg-destructive" },
};

/** A dot + label pill for a {label, description, tone} status; description shows on hover. */
export function ToneBadge({ meta, className }: { meta: StatusMeta; className?: string }) {
  const tone = TONE_CLASS[meta.tone];
  const badge = (
    <Badge className={cn("gap-1.5", tone.badge, className)}>
      <span aria-hidden className={cn("size-1.5 rounded-full", tone.dot)} />
      {meta.label}
    </Badge>
  );
  if (!meta.description) return badge;
  return (
    <TooltipProvider delayDuration={300}>
      <Tooltip>
        <TooltipTrigger asChild>{badge}</TooltipTrigger>
        <TooltipContent className="max-w-64">{meta.description}</TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
}

/** Organization id → name lookup from the admin org list (empty while loading). */
export function useOrgNames(): Map<string, string> {
  const orgs = useOrgs();
  return useMemo(
    () => new Map((orgs.data ?? []).map((o) => [o.id, o.name] as const)),
    [orgs.data],
  );
}

/** An org name with its id on hover; falls back to the truncated id. */
export function OrgLabel({ id, names }: { id?: string; names: Map<string, string> }) {
  if (!id) return <span className="text-muted-foreground">—</span>;
  const name = names.get(id);
  return name ? (
    <span title={id} className="truncate">{name}</span>
  ) : (
    <span title={id} className="truncate font-mono text-xs text-muted-foreground">{middleTruncate(id)}</span>
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
