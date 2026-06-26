// Raw firehose pub/sub for the admin event monitor + session-scoped surfaces.
// FROZEN — owned by the foundation agent.
//
// Every decoded data frame is published here in addition to being applied to
// the query cache. The admin monitor subscribes to this (NOT the cache) so a
// large rolling event list never pollutes TanStack Query. Session-scoped
// surfaces filter the global stream by e.session client-side — they MUST NOT
// open a second ?session= socket (HTTP/1.1 6-connection limit; the server
// already streams all of the tenant's sessions when `session` is omitted).

import { useEffect, useState } from "react";
import type { EventEnvelope } from "../api/types";

type Listener = (e: EventEnvelope) => void;

const listeners = new Set<Listener>();

/** Publish a data frame to all subscribers (synchronous). */
export function publishEvent(e: EventEnvelope): void {
  for (const fn of listeners) {
    try {
      fn(e);
    } catch {
      // A misbehaving subscriber must not break the others.
    }
  }
}

/** Subscribe to the raw firehose; returns an unsubscribe function. */
export function subscribeEvents(fn: Listener): () => void {
  listeners.add(fn);
  return () => {
    listeners.delete(fn);
  };
}

const DEFAULT_RING = 500;

/**
 * React hook: keep a bounded ring buffer (newest-first) of events matching an
 * optional filter. Used by the admin monitor and any surface that wants a live
 * tail without touching the query cache.
 */
export function useEventBus(
  filter?: (e: EventEnvelope) => boolean,
  capacity: number = DEFAULT_RING,
): EventEnvelope[] {
  const [events, setEvents] = useState<EventEnvelope[]>([]);

  useEffect(() => {
    return subscribeEvents((e) => {
      if (filter && !filter(e)) return;
      setEvents((prev) => {
        const next = [e, ...prev];
        return next.length > capacity ? next.slice(0, capacity) : next;
      });
    });
    // filter/capacity are intentionally not deps: subscribers wanting a new
    // filter should remount. Keeping a stable subscription avoids dropping
    // events on every render.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return events;
}
