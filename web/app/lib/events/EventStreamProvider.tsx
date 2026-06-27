// Mounts the single NDJSON event-stream connection and drives the cache bridge.
// Owned by the foundation agent. Mounted once at the app shell, but it stays
// IDLE until a surface opts in.
//
// Lifecycle:
//   - `enabled` is reference-counted: it flips true while ≥1 mounted surface
//     holds a subscription (useEventStreamSubscription) and back to false when
//     the last one unmounts. Idle pages never open the socket; navigating
//     between two live surfaces keeps the count >0 so the connection is reused.
//   - reconnects use full-jitter backoff, capped at 30s, reset on clean open.
//   - `since` tracks the last DATA-frame id; the server dedups the boundary.
//   - a 45s watchdog (server pings ~20s) catches dead-but-open TCP.
//   - visibilitychange→visible and `online` trigger an immediate reconnect.
//   - after 3 failed reconnects (or offline) we enter polling: surfaces flip
//     refetchInterval; the stream keeps retrying at 30s and exits on first open.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { openEventStream, type StreamErrorKind } from "./stream";
import { applyEvent } from "./cacheBridge";
import {
  EventStreamContext,
  type EventStreamState,
  type StreamStatus,
} from "./useEventStream";

const WATCHDOG_MS = 45_000;
const MAX_BACKOFF_MS = 30_000;
const POLL_AFTER_FAILURES = 3;

function backoff(attempt: number): number {
  const base = Math.min(MAX_BACKOFF_MS, 500 * 2 ** attempt);
  return Math.floor(base * (0.5 + Math.random() * 0.5)); // full jitter
}

export function EventStreamProvider({
  children,
}: {
  children: React.ReactNode;
}) {
  const qc = useQueryClient();
  const [status, setStatus] = useState<StreamStatus>("idle");
  const [polling, setPolling] = useState(false);

  // Reference-counted opt-in: the socket is live only while ≥1 mounted surface
  // holds a subscription. Acquire/release are stable so subscribers can use
  // them as effect deps without re-running. Net-zero swaps during a route
  // transition (one release + one acquire) batch into a single re-render, so
  // the connection is never dropped when moving between two live surfaces.
  const [subscribers, setSubscribers] = useState(0);
  const enabled = subscribers > 0;
  const acquire = useCallback(() => setSubscribers((n) => n + 1), []);
  const release = useCallback(
    () => setSubscribers((n) => Math.max(0, n - 1)),
    [],
  );

  // Mutable refs survive reconnects without re-triggering effects.
  const lastEventId = useRef<string | null>(null);
  const attempts = useRef(0);
  const abortRef = useRef<AbortController | null>(null);
  const watchdog = useRef<ReturnType<typeof setTimeout> | null>(null);
  const reconnectTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const stopped = useRef(false);

  const clearWatchdog = useCallback(() => {
    if (watchdog.current) clearTimeout(watchdog.current);
    watchdog.current = null;
  }, []);

  const armWatchdog = useCallback(
    (onExpire: () => void) => {
      clearWatchdog();
      watchdog.current = setTimeout(onExpire, WATCHDOG_MS);
    },
    [clearWatchdog],
  );

  // connect is recreated rarely; it reads everything else off refs.
  const connect = useRef<() => void>(() => {});

  const scheduleReconnect = useCallback(() => {
    if (stopped.current) return;
    if (reconnectTimer.current) clearTimeout(reconnectTimer.current);
    const failures = attempts.current;
    if (failures >= POLL_AFTER_FAILURES || (typeof navigator !== "undefined" && !navigator.onLine)) {
      setPolling(true);
      setStatus("polling");
    } else {
      setStatus("reconnecting");
    }
    const delay = backoff(failures);
    reconnectTimer.current = setTimeout(() => connect.current(), delay);
  }, []);

  connect.current = useCallback(() => {
    if (stopped.current) return;
    abortRef.current?.abort();
    const ac = new AbortController();
    abortRef.current = ac;
    setStatus((s) => (s === "polling" ? "polling" : "connecting"));

    const resetWatchdog = () =>
      armWatchdog(() => {
        // Dead-but-open TCP: abort and reconnect.
        ac.abort();
        attempts.current += 1;
        scheduleReconnect();
      });
    resetWatchdog();

    void openEventStream({
      events: "*",
      since: lastEventId.current ?? undefined,
      signal: ac.signal,
      onEvent: (ev) => {
        // First successful frame = healthy connection.
        if (status !== "open") setStatus("open");
        attempts.current = 0;
        if (polling) setPolling(false);
        lastEventId.current = ev.id;
        resetWatchdog();
        applyEvent(qc, ev);
      },
      onPing: () => {
        if (status !== "open") setStatus("open");
        attempts.current = 0;
        if (polling) setPolling(false);
        resetWatchdog();
      },
      onError: (kind: StreamErrorKind, statusCode?: number) => {
        clearWatchdog();
        if (ac.signal.aborted) return;
        if (kind === "http" && statusCode === 401) {
          // Unauthenticated: stop and let the query layer redirect to login.
          stopped.current = true;
          setStatus("closed");
          return;
        }
        if (kind === "replay_failed") {
          // Drop the cursor, reconnect fresh, then resync mounted surfaces.
          lastEventId.current = null;
          attempts.current += 1;
          scheduleReconnect();
          void qc.invalidateQueries();
          return;
        }
        attempts.current += 1;
        scheduleReconnect();
      },
    });
    // status/polling are read fresh via closure each connect; deps kept minimal.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [armWatchdog, clearWatchdog, qc, scheduleReconnect]);

  const reconnectNow = useCallback(() => {
    if (stopped.current) return;
    if (reconnectTimer.current) clearTimeout(reconnectTimer.current);
    attempts.current = 0;
    connect.current();
  }, []);

  // Start/stop the connection with `enabled`.
  useEffect(() => {
    if (!enabled) {
      stopped.current = true;
      abortRef.current?.abort();
      clearWatchdog();
      if (reconnectTimer.current) clearTimeout(reconnectTimer.current);
      setStatus("idle");
      return;
    }
    stopped.current = false;
    attempts.current = 0;
    connect.current();
    return () => {
      stopped.current = true;
      abortRef.current?.abort();
      clearWatchdog();
      if (reconnectTimer.current) clearTimeout(reconnectTimer.current);
    };
  }, [enabled, clearWatchdog]);

  // visibility + online/offline handling.
  useEffect(() => {
    if (!enabled) return;
    const onVisible = () => {
      if (document.visibilityState === "visible") reconnectNow();
    };
    const onOnline = () => reconnectNow();
    const onOffline = () => {
      setPolling(true);
      setStatus("polling");
    };
    document.addEventListener("visibilitychange", onVisible);
    window.addEventListener("online", onOnline);
    window.addEventListener("offline", onOffline);
    return () => {
      document.removeEventListener("visibilitychange", onVisible);
      window.removeEventListener("online", onOnline);
      window.removeEventListener("offline", onOffline);
    };
  }, [enabled, reconnectNow]);

  const value = useMemo<EventStreamState>(
    () => ({
      status,
      lastEventId: lastEventId.current,
      polling,
      reconnectNow,
      active: enabled,
      acquire,
      release,
    }),
    [status, polling, reconnectNow, enabled, acquire, release],
  );

  return (
    <EventStreamContext.Provider value={value}>
      {children}
    </EventStreamContext.Provider>
  );
}
