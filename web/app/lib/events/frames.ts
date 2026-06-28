// Realtime frame types + type-guards. Realtime is a single WebSocket on the
// central router (docs/specs/router.md), which delivers discrete JSON messages —
// there is no byte-stream framing to parse. These are the shapes the WS client
// classifies each parsed message into.

import type { EventEnvelope } from "../api/types";

/**
 * First frame on a freshly-opened stream, before any replay/tail. Confirms the
 * stream is live at once (no id/payload) and advertises the heartbeat cadence.
 */
export type ConnectedFrame = { event: "connected"; heartbeatSeconds?: number };

/** Periodic heartbeat sent ~every 20s. No id/payload; resets the watchdog. */
export type PingFrame = { event: "ping" };

/** In-band error frame emitted after the stream opens (e.g. since-replay failed). */
export type ErrorFrame = { event: "error"; error: string };

/** One decoded frame: a data envelope, a connected/ping signal, or an error. */
export type StreamFrame = EventEnvelope | ConnectedFrame | PingFrame | ErrorFrame;

export function isConnectedFrame(f: StreamFrame): f is ConnectedFrame {
  return (f as ConnectedFrame).event === "connected" && !("id" in f);
}

export function isPingFrame(f: StreamFrame): f is PingFrame {
  return (f as PingFrame).event === "ping" && !("id" in f);
}

export function isErrorFrame(f: StreamFrame): f is ErrorFrame {
  return (f as ErrorFrame).event === "error" && typeof (f as ErrorFrame).error === "string";
}

export function isDataFrame(f: StreamFrame): f is EventEnvelope {
  return (
    !isConnectedFrame(f) &&
    !isPingFrame(f) &&
    !isErrorFrame(f) &&
    typeof (f as EventEnvelope).id === "string"
  );
}
