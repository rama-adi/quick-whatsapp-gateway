// Event-stream transport: opens GET {GATEWAY_URL}/api/v1/events as an NDJSON
// ReadableStream against the gateway DIRECTLY (R4) and dispatches decoded frames.
//
// v2 shape (§4, §12): the browser connects to the gateway with a Bearer JWT (NOT
// the cookie session — the gateway is cross-origin). The existing fetch +
// ReadableStream logic is preserved; only the URL + auth changed:
//   - apiUrl() now points at VITE_GATEWAY_URL/api/v1.
//   - we attach `Authorization: Bearer <jwt>` from the token provider; no
//     credentials:"include".
//   - the JWT is short-lived (5 min); the consumer (useEventStream) refreshes it
//     and reconnects via since={lastEventId} (§4.7) so a token roll never tears
//     the view.
//
// Verified against internal/stream/handler.go:
//   - the filter param is `events` (NOT `types`); default "*".
//   - `since={id}` replays event_log oldest-first and dedups the boundary,
//     so callers do NOT need client-side dedup on normal reconnects.
//   - the stream opens with a {"event":"connected","heartbeatSeconds":N} line.
//   - heartbeat lines are {"event":"ping"} (~20s), no id/payload.
//   - an in-band {"event":"error"} line signals replay/stream failure.

import { apiUrl } from "../api/client";
import { getGatewayToken } from "../api/token-provider";
import type { EventEnvelope } from "../api/types";
import { parseNdjson, isConnectedFrame, isPingFrame, isErrorFrame } from "./ndjson";

/** Why the stream ended; the provider maps these to reconnect/polling. */
export type StreamErrorKind = "replay_failed" | "http" | "eof" | "network";

export interface OpenEventStreamOptions {
  /** Event-type filter; default "*" (all). Sent as ?events=. */
  events?: string;
  /** Restrict to a single session id; omit for all of the tenant's sessions. */
  session?: string;
  /** Replay cursor — last data-frame id seen. Sent as ?since= on reconnect. */
  since?: string;
  signal: AbortSignal;
  /** Called for every data frame (NOT connected/pings/errors). */
  onEvent: (e: EventEnvelope) => void;
  /**
   * Liveness signal — the leading `connected` frame and every heartbeat `ping`.
   * Resets the watchdog and marks the connection healthy; never updates `since`.
   */
  onPing: () => void;
  /** Called once when the stream terminates for any reason. */
  onError: (kind: StreamErrorKind, status?: number) => void;
}

/**
 * Open the stream and pump frames until it ends or the signal aborts. Resolves
 * when the stream is done; rejects nothing — terminal conditions are reported
 * via onError so the caller has one code path for reconnection.
 */
export async function openEventStream(o: OpenEventStreamOptions): Promise<void> {
  const qs = new URLSearchParams();
  qs.set("events", o.events ?? "*");
  if (o.session) qs.set("session", o.session);
  if (o.since) qs.set("since", o.since);

  const headers: Record<string, string> = { Accept: "application/x-ndjson" };
  const token = await getGatewayToken();
  if (token) headers.Authorization = `Bearer ${token}`;

  let res: Response;
  try {
    res = await fetch(apiUrl(`/events?${qs.toString()}`), {
      method: "GET",
      headers,
      signal: o.signal,
    });
  } catch (err) {
    if (o.signal.aborted) return;
    o.onError("network");
    return;
  }

  if (!res.ok) {
    o.onError("http", res.status);
    return;
  }
  if (!res.body) {
    o.onError("http", res.status);
    return;
  }

  try {
    for await (const frame of parseNdjson(res.body, o.signal)) {
      // The leading `connected` frame and periodic pings are both liveness-only:
      // they prove the socket is open without carrying a data event or cursor.
      if (isConnectedFrame(frame) || isPingFrame(frame)) {
        o.onPing();
        continue;
      }
      if (isErrorFrame(frame)) {
        o.onError("replay_failed");
        return;
      }
      // Data frame.
      o.onEvent(frame);
    }
    // Generator completed without an explicit error frame: the server closed.
    if (!o.signal.aborted) {
      o.onError("eof");
    }
  } catch (err) {
    if (o.signal.aborted) return;
    o.onError("network");
  }
}
