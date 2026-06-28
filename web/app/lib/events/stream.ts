// Event-stream transport: the realtime channel is a single WebSocket on the
// CENTRAL ROUTER (docs/specs/router.md, plan D5). A browser WebSocket cannot set
// an Authorization header, so authorization happens once at ticket mint (a normal
// bearer-authenticated POST) and the WS handshake merely redeems a short-lived,
// single-use ticket carried in the URL.
//
// Flow:
//   1. POST {ROUTER}/api/v1/realtime/ticket with the bearer JWT + requested scope
//      (session or organization), event filter, and ?since cursor. The router
//      authorizes the scope and returns a single-use ticket.
//   2. Open ws(s)://{ROUTER}/api/v1/realtime?ticket=… and pump frames.
//
// The frame shapes are unchanged from the previous NDJSON transport
// (connected/ping/error/data), so the EventStreamProvider above is untouched:
//   - opens with {"event":"connected","heartbeatSeconds":N}
//   - heartbeat {"event":"ping"} (~20s)
//   - in-band {"event":"error"} signals replay/stream failure
//   - data frames are full event envelopes (carry an id used as the ?since cursor)

import { apiUrl, ApiError, fetchJSON } from "../api/client";
import type { EventEnvelope } from "../api/types";
import {
  isConnectedFrame,
  isPingFrame,
  isErrorFrame,
  type StreamFrame,
} from "./frames";

/** Why the stream ended; the provider maps these to reconnect/polling. */
export type StreamErrorKind = "replay_failed" | "http" | "eof" | "network";

export interface OpenEventStreamOptions {
  /** Event-type filter; default "*" (all). */
  events?: string;
  /** Restrict to a single session id; omit for all of the tenant's sessions. */
  session?: string;
  /** Replay cursor — last data-frame id seen; replayed via the ticket's since. */
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

interface TicketResponse {
  ticket: string;
  expiresInSeconds: number;
  url: string;
}

/**
 * Mint a ticket then open the WebSocket and pump frames until it closes or the
 * signal aborts. Resolves when done; terminal conditions are reported via onError
 * so the caller has one reconnection code path.
 */
export async function openEventStream(o: OpenEventStreamOptions): Promise<void> {
  // 1) Mint a single-use ticket (bearer-authenticated; authz happens here).
  let ticket: TicketResponse;
  try {
    ticket = await fetchJSON<TicketResponse>(apiUrl("/realtime/ticket"), {
      method: "POST",
      body: JSON.stringify({
        scope: o.session ? "session" : "organization",
        session: o.session,
        events: (o.events ?? "*").split(",").map((s) => s.trim()).filter(Boolean),
        since: o.since,
      }),
    });
  } catch (err) {
    if (o.signal.aborted) return;
    o.onError("http", err instanceof ApiError ? err.status : undefined);
    return;
  }

  if (o.signal.aborted) return;

  // 2) Redeem the ticket over the WebSocket. Build the ws(s):// URL from our own
  // API base (not the server-advertised url, which may use an internal hostname).
  const wsUrl = apiUrl(`/realtime?ticket=${encodeURIComponent(ticket.ticket)}`).replace(
    /^http/,
    "ws",
  );

  return new Promise<void>((resolve) => {
    let settled = false;
    const finish = (kind?: StreamErrorKind, status?: number): void => {
      if (settled) return;
      settled = true;
      o.signal.removeEventListener("abort", onAbort);
      if (kind && !o.signal.aborted) o.onError(kind, status);
      resolve();
    };

    let socket: WebSocket;
    try {
      socket = new WebSocket(wsUrl);
    } catch {
      o.onError("network");
      resolve();
      return;
    }

    const onAbort = (): void => {
      try {
        socket.close();
      } catch {
        /* ignore */
      }
      finish();
    };
    o.signal.addEventListener("abort", onAbort, { once: true });

    socket.onmessage = (ev: MessageEvent): void => {
      const frame = tryParse(typeof ev.data === "string" ? ev.data : "");
      if (!frame) return;
      if (isConnectedFrame(frame) || isPingFrame(frame)) {
        o.onPing();
        return;
      }
      if (isErrorFrame(frame)) {
        try {
          socket.close();
        } catch {
          /* ignore */
        }
        finish("replay_failed");
        return;
      }
      o.onEvent(frame);
    };

    socket.onerror = (): void => {
      finish("network");
    };

    socket.onclose = (): void => {
      // A clean server close with no prior error frame looks like EOF; the
      // provider reconnects with the cached since cursor.
      finish("eof");
    };
  });
}

function tryParse(line: string): StreamFrame | null {
  if (!line) return null;
  try {
    const obj = JSON.parse(line) as unknown;
    if (obj && typeof obj === "object") return obj as StreamFrame;
    return null;
  } catch {
    return null;
  }
}
