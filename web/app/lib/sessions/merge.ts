// One ordering rule for a cached session row, shared by every writer: query
// fetch results, lifecycle mutation responses and session.status events.
//
// A session row reaches the cache three ways, and they can land in any order:
//   - a REST read (GET /sessions/{id}, GET /sessions, GET /admin/sessions),
//   - a lifecycle action response (POST /sessions/{id}:{action}),
//   - a session.status event.
// Without a rule, the last write wins, so a GET that read the row before the
// API projected an event can land after that event and roll the status back.
//
// The rule: keep the newer side. A REST row is as new as its updatedAt (the API
// stamps it when it projects a session.status event). An event is as new as its
// envelope timestamp; applying it sets the row's updatedAt to that timestamp, so
// later REST rows compare against it.
//
// Clock skew: updatedAt comes from the API's clock and event timestamps from
// the gateway's. To keep skew from hiding real data:
//   - an older REST row whose status equals the cached status is still taken
//     (its other fields, like a newly attached waJid, are current), keeping the
//     newer updatedAt;
//   - an event older than the cached row is dropped, and the caller refetches
//     the row so REST decides (see cacheBridge).
// When the backend adds a per-session status sequence, compare that here
// instead of timestamps; every writer goes through these functions.

import { replaceEqualDeep, type InfiniteData, type QueryClient } from "@tanstack/react-query";
import { qk } from "../query";
import type { Page } from "../api/envelope";
import type { WASession } from "../api/types";

export type SessionPages = InfiniteData<Page<WASession>, string | undefined>;

/** Merge an incoming REST row (fetch or mutation response) into the cached row. */
export function mergeSession(
  current: WASession | undefined,
  incoming: WASession,
): WASession {
  if (!current || current.id !== incoming.id) return incoming;
  if (incoming.updatedAt >= current.updatedAt) return incoming;
  if (incoming.status === current.status) {
    return { ...incoming, updatedAt: current.updatedAt };
  }
  return current;
}

/** Whether an event stamped `at` is at least as new as the cached row. */
export function eventIsCurrent(current: WASession | undefined, at: number): boolean {
  return !current || at >= current.updatedAt;
}

/**
 * Apply an event's change to a cached row when the event is current. Returns
 * the row unchanged when the event is older than it.
 */
export function applySessionEvent(
  current: WASession,
  at: number,
  change: (row: WASession) => WASession,
): WASession {
  if (!eventIsCurrent(current, at)) return current;
  return { ...change(current), updatedAt: at };
}

/** Merge a fetched list into the cached list, row by row (matched by id). */
export function mergeSessionPages(
  current: SessionPages | undefined,
  incoming: SessionPages,
): SessionPages {
  if (!current) return incoming;
  const cached = new Map<string, WASession>();
  for (const page of current.pages) {
    for (const row of page.data) cached.set(row.id, row);
  }
  return {
    ...incoming,
    pages: incoming.pages.map((page) => ({
      ...page,
      data: page.data.map((row) => mergeSession(cached.get(row.id), row)),
    })),
  };
}

/**
 * structuralSharing for a single-session query: every write (fetch result or
 * setQueryData) goes through mergeSession, then keeps unchanged references.
 */
export function sessionStructuralSharing(oldData: unknown, newData: unknown): unknown {
  const merged = mergeSession(oldData as WASession | undefined, newData as WASession);
  return replaceEqualDeep(oldData, merged);
}

/** structuralSharing for a session list (infinite) query. */
export function sessionPagesStructuralSharing(oldData: unknown, newData: unknown): unknown {
  const merged = mergeSessionPages(oldData as SessionPages | undefined, newData as SessionPages);
  return replaceEqualDeep(oldData, merged);
}

/**
 * Write a session row from a mutation response (a lifecycle action) into the
 * detail and list caches through mergeSession, so a response that the API
 * built before a newer event cannot replace that event's status.
 */
export function writeSessionResponse(qc: QueryClient, data: WASession): void {
  qc.setQueryData<WASession>(qk.session(data.id), (current) => mergeSession(current, data));
  for (const key of [qk.sessions(), qk.adminSessions()]) {
    qc.setQueryData<SessionPages>(key, (list) =>
      list
        ? {
            ...list,
            pages: list.pages.map((page) => ({
              ...page,
              data: page.data.map((row) => (row.id === data.id ? mergeSession(row, data) : row)),
            })),
          }
        : list,
    );
  }
}
