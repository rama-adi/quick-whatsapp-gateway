// The wait-stream driver: connect → read NDJSON → surface status transitions,
// with automatic reconnect on a mid-flight drop (oauth.md §6.1 step 4 — the
// server re-emits the current snapshot, so reconnects are idempotent).
//
// This is framework-agnostic (no React) so it is unit-testable against a
// synthetic ReadableStream. The React route wraps it in a hook.

import {
  isHeartbeat,
  isPending,
  isTerminal,
  oauthUrl,
  readNdjson,
  type FinalizeResponse,
  type PendingSnapshot,
  type StatusFrame,
} from "./protocol";

/** Terminal outcomes the page renders as final (no retry with the same code). */
export type Terminal = "verified" | "denied" | "expired";

export interface WaitCallbacks {
  /** First snapshot (and any re-emitted snapshot after reconnect). */
  onSnapshot: (snap: PendingSnapshot) => void;
  /** Any heartbeat/data frame — liveness signal (resets a staleness UI). */
  onLive: () => void;
  /** A terminal transition arrived; the driver stops after this. */
  onTerminal: (t: Terminal) => void;
  /** The stream 404'd (unknown/expired browser code) — unrecoverable. */
  onNotFound: () => void;
  /** A transient failure the driver is retrying (shows a "reconnecting" hint). */
  onReconnecting: () => void;
}

export interface WaitDriverOptions extends WaitCallbacks {
  browserCode: string;
  signal: AbortSignal;
  /** Injectable for tests; defaults to global fetch. */
  fetchImpl?: typeof fetch;
  /** Base backoff between reconnect attempts (ms). Tests override to 0. */
  backoffMs?: number;
}

/**
 * Open the wait stream and keep it open until a terminal status, a 404, or the
 * signal aborts. Reconnects on transient drops (EOF/network) with a small
 * capped backoff. Resolves when it stops driving (terminal / 404 / abort).
 */
export async function driveWaitStream(o: WaitDriverOptions): Promise<void> {
  const doFetch = o.fetchImpl ?? fetch;
  const url = oauthUrl(`/oauth/wait/${encodeURIComponent(o.browserCode)}/stream`);
  const baseBackoff = o.backoffMs ?? 1000;
  let attempt = 0;
  let done = false;

  const handle = (frame: StatusFrame): void => {
    if (isHeartbeat(frame)) {
      o.onLive();
      return;
    }
    if (isPending(frame)) {
      attempt = 0; // a good snapshot means the connection is healthy
      o.onLive();
      o.onSnapshot(frame);
      return;
    }
    if (isTerminal(frame)) {
      o.onLive();
      done = true;
      o.onTerminal(frame.status);
    }
  };

  while (!done && !o.signal.aborted) {
    try {
      const res = await doFetch(url, {
        method: "GET",
        headers: { Accept: "application/x-ndjson" },
        signal: o.signal,
        // Public capability endpoint — never send cookies or credentials.
        credentials: "omit",
        cache: "no-store",
      });

      if (res.status === 404) {
        o.onNotFound();
        return;
      }
      if (!res.ok || !res.body) {
        throw new Error(`stream http ${res.status}`);
      }

      const reader = res.body.getReader();
      try {
        await readNdjson(reader, handle);
      } finally {
        try {
          reader.releaseLock();
        } catch {
          /* already released */
        }
      }
      // Clean EOF without a terminal frame → the server dropped us; reconnect.
    } catch (err) {
      if (o.signal.aborted) return;
      // fall through to backoff + retry
      void err;
    }

    if (done || o.signal.aborted) return;

    // Transient drop → reconnect (server re-emits the snapshot). Backoff, capped.
    o.onReconnecting();
    attempt += 1;
    const delay = Math.min(baseBackoff * attempt, 5000);
    await sleep(delay, o.signal);
  }
}

/** POST /oauth/wait/{code}/finalize → the fully-built redirect URL. */
export async function finalize(
  browserCode: string,
  signal?: AbortSignal,
  fetchImpl: typeof fetch = fetch,
): Promise<FinalizeResponse> {
  const res = await fetchImpl(
    oauthUrl(`/oauth/wait/${encodeURIComponent(browserCode)}/finalize`),
    {
      method: "POST",
      headers: { Accept: "application/json" },
      credentials: "omit",
      cache: "no-store",
      signal,
    },
  );
  if (!res.ok) throw new Error(`finalize http ${res.status}`);
  return (await res.json()) as FinalizeResponse;
}

/** POST /oauth/wait/{code}/cancel — "This isn't me". Best-effort. */
export async function cancel(
  browserCode: string,
  fetchImpl: typeof fetch = fetch,
): Promise<void> {
  await fetchImpl(oauthUrl(`/oauth/wait/${encodeURIComponent(browserCode)}/cancel`), {
    method: "POST",
    headers: { Accept: "application/json" },
    credentials: "omit",
    cache: "no-store",
  });
}

function sleep(ms: number, signal: AbortSignal): Promise<void> {
  if (ms <= 0) return Promise.resolve();
  return new Promise((resolve) => {
    const t = setTimeout(() => {
      signal.removeEventListener("abort", onAbort);
      resolve();
    }, ms);
    const onAbort = (): void => {
      clearTimeout(t);
      resolve();
    };
    signal.addEventListener("abort", onAbort, { once: true });
  });
}
