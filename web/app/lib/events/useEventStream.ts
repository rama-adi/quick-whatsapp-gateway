// The single-connection event-stream controller + React context.
// FROZEN — owned by the foundation agent. Surface agents read status via
// useEventStream(); they never open their own connection.

import { createContext, useContext } from "react";

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
}

export const EventStreamContext = createContext<EventStreamState>({
  status: "idle",
  lastEventId: null,
  polling: false,
  reconnectNow: () => {},
});

/** Read the live stream status anywhere under the provider. */
export function useEventStream(): EventStreamState {
  return useContext(EventStreamContext);
}

/** Convenience: components can gate refetchInterval on the polling flag. */
export function usePollingInterval(ms = 5000): number | false {
  return useEventStream().polling ? ms : false;
}
