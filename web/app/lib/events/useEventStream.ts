// The single-connection event-stream controller + React context.
// Owned by the foundation agent. Surface agents read status via
// useEventStream(); they never open their own connection.
//
// The connection is REFERENCE-COUNTED and page-scoped: the provider keeps one
// shared NDJSON socket open only while ≥1 mounted surface holds a subscription
// (via useEventStreamSubscription), and tears it down when the last unmounts.
// This keeps idle pages (keys, webhooks, dashboard, docs) off the socket while
// staying a single connection for any surfaces that do need live events.

import { createContext, useContext, useEffect } from "react";

export type StreamStatus =
  | "idle"
  | "connecting"
  | "open"
  | "reconnecting"
  | "polling"
  | "closed";

export interface EventStreamState {
  status: StreamStatus;
  /** Last data-frame id seen (the reconnect cursor). */
  lastEventId: string | null;
  /** True while polling fallback is active — surfaces flip refetchInterval. */
  polling: boolean;
  /** Force an immediate reconnect attempt (e.g. user clicked "retry"). */
  reconnectNow: () => void;
  /** True while ≥1 mounted surface has requested the stream (socket is live). */
  active: boolean;
  /** @internal registration handle used by useEventStreamSubscription. */
  acquire: () => void;
  /** @internal registration handle used by useEventStreamSubscription. */
  release: () => void;
}

export const EventStreamContext = createContext<EventStreamState>({
  status: "idle",
  lastEventId: null,
  polling: false,
  reconnectNow: () => {},
  active: false,
  acquire: () => {},
  release: () => {},
});

/** Read the live stream status anywhere under the provider. */
export function useEventStream(): EventStreamState {
  return useContext(EventStreamContext);
}

/**
 * Request the live event stream for as long as the calling component is mounted.
 *
 * This is the modular opt-in: drop it into any page or component that needs live
 * events (sessions, chats, the admin monitor / sessions table) and the provider
 * opens its single shared socket. Pages that don't call it never open the
 * connection. Reference-counted at the provider, so navigating between two live
 * surfaces hands the connection over without dropping it.
 */
export function useEventStreamSubscription(): void {
  const { acquire, release } = useContext(EventStreamContext);
  useEffect(() => {
    acquire();
    return release;
  }, [acquire, release]);
}

/** Convenience: components can gate refetchInterval on the polling flag. */
export function usePollingInterval(ms = 5000): number | false {
  return useEventStream().polling ? ms : false;
}
