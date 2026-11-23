// useSessionView: the one view of a session that pages render from. It builds
// the SessionViewInput from the cache (merged row + pairing artifacts) and
// derives the state with deriveSessionView (view.ts). Pending lifecycle
// actions come back as `pending`, an overlay that never decides the state.

import { useEffect, useRef, useState } from "react";
import { useMutationState, type UseQueryOptions } from "@tanstack/react-query";
import {
  sessionMutationKeys,
  useSession,
  useSessionPairingCode,
  useSessionPasskey,
  useSessionPendingAction,
  useSessionQR,
} from "../api/hooks/sessions";
import type { ApiError } from "../api/envelope";
import type { SessionAction, SessionStatus, WASession } from "../api/types";
import {
  deriveSessionView,
  nextViewDeadline,
  type SessionView,
  type SessionViewInput,
} from "./view";

/** How long a finished action stays "in progress" while its effect is not visible yet. */
export const SETTLE_MS = 15_000;

export type SessionActionOverlay = SessionAction | "delete" | undefined;

export interface SessionViewResult {
  view: SessionView;
  /** The merged row, when one is cached. */
  session: WASession | undefined;
  /** The action in flight (or just answered, until its effect shows). */
  pending: SessionActionOverlay;
}

/** Statuses that show each action has taken effect. */
const EFFECT: Record<SessionAction, ReadonlySet<SessionStatus>> = {
  start: new Set(["starting", "scan_qr_code", "working", "reconnecting"]),
  restart: new Set(["starting", "scan_qr_code", "working", "reconnecting"]),
  stop: new Set(["stopped", "logged_out", "failed"]),
  logout: new Set(["logged_out"]),
};

type SettledAction = { action: SessionAction; data: WASession | undefined; submittedAt: number };

/**
 * A lifecycle response can arrive before its effect: :start answers with the
 * row as it was (the gateway reports starting later, as an event). Keep the
 * action as the overlay until the row shows the effect, a newer row arrives,
 * or SETTLE_MS passes, so the UI does not flash back to "Start the session".
 */
export function settlingAction(
  row: WASession | undefined,
  latest: SettledAction | undefined,
  now: number,
): SessionAction | undefined {
  if (!row || !latest?.data) return undefined;
  if (now - latest.submittedAt >= SETTLE_MS) return undefined;
  if (row.updatedAt > latest.data.updatedAt) return undefined;
  if (EFFECT[latest.action].has(row.status)) return undefined;
  return latest.action;
}

/** Re-render at `deadline` (epoch ms); returns the current time. */
function useNow(deadline: number | undefined): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (deadline === undefined) return;
    const wait = Math.max(0, deadline - Date.now()) + 50;
    const t = setTimeout(() => setNow(Date.now()), wait);
    return () => clearTimeout(t);
  }, [deadline]);
  return now;
}

/**
 * The view for a session row the caller already has (the admin pages read
 * rows from the admin list). `loading`/`error` describe that row's query.
 */
export function useSessionViewFromRow(
  sessionId: string,
  row: WASession | undefined,
  { loading = false, error }: { loading?: boolean; error?: unknown } = {},
): SessionViewResult {
  const awaiting = row?.status === "scan_qr_code";
  const qr = useSessionQR(sessionId, awaiting).data;
  const pairingCode = useSessionPairingCode(sessionId);
  const passkey = useSessionPasskey(sessionId);
  const inFlight = useSessionPendingAction(sessionId);
  const latest = useMutationState({
    filters: {
      mutationKey: sessionMutationKeys.lifecycle,
      status: "success",
      predicate: (m) =>
        (m.state.variables as { sessionId?: string } | undefined)?.sessionId === sessionId,
    },
    select: (m): SettledAction => ({
      action: (m.state.variables as { action: SessionAction }).action,
      data: m.state.data as WASession | undefined,
      submittedAt: m.state.submittedAt,
    }),
  }).at(-1);

  // When the row first showed working without an identity. The identity
  // refetch can fail, so "linking" is bounded: after LINKING_STALL_MS the view
  // reports it stalled and pages offer a retry.
  const linking = row?.status === "working" && !row.waJid;
  const linkingSince = useRef<number | undefined>(undefined);
  if (!linking) linkingSince.current = undefined;
  else linkingSince.current ??= Date.now();

  const draft: Omit<SessionViewInput, "now"> = {
    session: row,
    loading,
    error,
    qr,
    pairingCode,
    passkey,
    linkingSince: linkingSince.current,
  };
  const renderedAt = Date.now();
  const settleEnd = latest ? latest.submittedAt + SETTLE_MS : undefined;
  const now = useNow(
    minDefined(
      nextViewDeadline({ ...draft, now: renderedAt }),
      settleEnd !== undefined && settleEnd > renderedAt ? settleEnd : undefined,
    ),
  );
  const input: SessionViewInput = { ...draft, now };

  return {
    view: deriveSessionView(input),
    session: row,
    pending: inFlight ?? settlingAction(row, latest, now),
  };
}

/** The view for a session in the caller's organization (GET /sessions/{id}). */
export function useSessionView(
  sessionId: string,
  refetchInterval?: UseQueryOptions<WASession, ApiError>["refetchInterval"],
): SessionViewResult & { query: ReturnType<typeof useSession> } {
  const query = useSession(sessionId, refetchInterval);
  const result = useSessionViewFromRow(sessionId, query.data, {
    loading: query.isLoading,
    error: query.error ?? undefined,
  });
  return { ...result, query };
}

function minDefined(a: number | undefined, b: number | undefined): number | undefined {
  if (a === undefined) return b;
  if (b === undefined) return a;
  return Math.min(a, b);
}
