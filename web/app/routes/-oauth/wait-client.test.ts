import { describe, it, expect, vi } from "vitest";
import { cancel, driveWaitStream, finalize, type Terminal } from "./wait-client";
import type { PendingSnapshot } from "./protocol";

const PENDING: PendingSnapshot = {
  status: "pending",
  app: { name: "Acme", logo: null },
  user_code: "483920",
  login_command: "login",
  target: { mode: "dm", number: "+6281234" },
  scopes: ["openid", "profile", "offline_access"],
  expires_at: Date.now() + 600_000,
};

/** A 200 NDJSON Response whose body streams `lines` (each JSON stringified). */
function ndjsonResponse(lines: object[]): Response {
  const text = lines.map((l) => JSON.stringify(l)).join("\n") + "\n";
  const body = new ReadableStream<Uint8Array>({
    start(controller) {
      controller.enqueue(new TextEncoder().encode(text));
      controller.close();
    },
  });
  return new Response(body, { status: 200, headers: { "content-type": "application/x-ndjson" } });
}

function collectors() {
  const snapshots: PendingSnapshot[] = [];
  const state = { terminal: null as Terminal | null, notFound: false, reconnects: 0, live: 0 };
  const cb = {
    onSnapshot: (s: PendingSnapshot) => {
      snapshots.push(s);
    },
    onLive: () => {
      state.live += 1;
    },
    onTerminal: (t: Terminal) => {
      state.terminal = t;
    },
    onNotFound: () => {
      state.notFound = true;
    },
    onReconnecting: () => {
      state.reconnects += 1;
    },
  };
  return {
    snapshots,
    get terminal() {
      return state.terminal;
    },
    get notFound() {
      return state.notFound;
    },
    get reconnects() {
      return state.reconnects;
    },
    get live() {
      return state.live;
    },
    cb,
  };
}

describe("driveWaitStream", () => {
  it("pending → verified", async () => {
    const c = collectors();
    const fetchImpl = vi.fn().mockResolvedValue(
      ndjsonResponse([PENDING, { status: "heartbeat" }, { status: "verified" }]),
    );
    await driveWaitStream({
      browserCode: "code",
      signal: new AbortController().signal,
      fetchImpl: fetchImpl as unknown as typeof fetch,
      backoffMs: 0,
      ...c.cb,
    });
    expect(c.snapshots).toHaveLength(1);
    expect(c.snapshots[0]!.user_code).toBe("483920");
    expect(c.terminal).toBe("verified");
    expect(fetchImpl).toHaveBeenCalledTimes(1);
  });

  it("pending → denied", async () => {
    const c = collectors();
    const fetchImpl = vi.fn().mockResolvedValue(ndjsonResponse([PENDING, { status: "denied" }]));
    await driveWaitStream({
      browserCode: "code",
      signal: new AbortController().signal,
      fetchImpl: fetchImpl as unknown as typeof fetch,
      backoffMs: 0,
      ...c.cb,
    });
    expect(c.terminal).toBe("denied");
  });

  it("pending → expired", async () => {
    const c = collectors();
    const fetchImpl = vi.fn().mockResolvedValue(ndjsonResponse([PENDING, { status: "expired" }]));
    await driveWaitStream({
      browserCode: "code",
      signal: new AbortController().signal,
      fetchImpl: fetchImpl as unknown as typeof fetch,
      backoffMs: 0,
      ...c.cb,
    });
    expect(c.terminal).toBe("expired");
  });

  it("404 → onNotFound, no retry", async () => {
    const c = collectors();
    const fetchImpl = vi.fn().mockResolvedValue(new Response(null, { status: 404 }));
    await driveWaitStream({
      browserCode: "gone",
      signal: new AbortController().signal,
      fetchImpl: fetchImpl as unknown as typeof fetch,
      backoffMs: 0,
      ...c.cb,
    });
    expect(c.notFound).toBe(true);
    expect(c.terminal).toBeNull();
    expect(fetchImpl).toHaveBeenCalledTimes(1);
  });

  it("reconnects after a mid-flight EOF, re-emits snapshot, then terminates", async () => {
    const c = collectors();
    // 1st connection: snapshot then clean EOF (no terminal) → driver reconnects.
    // 2nd connection: re-emitted snapshot + verified.
    const fetchImpl = vi
      .fn()
      .mockResolvedValueOnce(ndjsonResponse([PENDING]))
      .mockResolvedValueOnce(ndjsonResponse([PENDING, { status: "verified" }]));
    await driveWaitStream({
      browserCode: "code",
      signal: new AbortController().signal,
      fetchImpl: fetchImpl as unknown as typeof fetch,
      backoffMs: 0,
      ...c.cb,
    });
    expect(fetchImpl).toHaveBeenCalledTimes(2);
    expect(c.reconnects).toBeGreaterThanOrEqual(1);
    expect(c.snapshots).toHaveLength(2); // re-emitted on reconnect (idempotent)
    expect(c.terminal).toBe("verified");
  });

  it("retries on a thrown network error then succeeds", async () => {
    const c = collectors();
    const fetchImpl = vi
      .fn()
      .mockRejectedValueOnce(new Error("network"))
      .mockResolvedValueOnce(ndjsonResponse([PENDING, { status: "verified" }]));
    await driveWaitStream({
      browserCode: "code",
      signal: new AbortController().signal,
      fetchImpl: fetchImpl as unknown as typeof fetch,
      backoffMs: 0,
      ...c.cb,
    });
    expect(fetchImpl).toHaveBeenCalledTimes(2);
    expect(c.terminal).toBe("verified");
  });

  it("stops immediately when the signal is already aborted", async () => {
    const c = collectors();
    const ctrl = new AbortController();
    ctrl.abort();
    const fetchImpl = vi.fn();
    await driveWaitStream({
      browserCode: "code",
      signal: ctrl.signal,
      fetchImpl: fetchImpl as unknown as typeof fetch,
      backoffMs: 0,
      ...c.cb,
    });
    expect(fetchImpl).not.toHaveBeenCalled();
  });
});

describe("finalize / cancel", () => {
  it("finalize returns the redirect payload", async () => {
    const fetchImpl = vi
      .fn()
      .mockResolvedValue(
        new Response(JSON.stringify({ redirect: "https://app/cb?code=x&state=y" }), {
          status: 200,
        }),
      );
    const res = await finalize("code", undefined, fetchImpl as unknown as typeof fetch);
    expect(res.redirect).toBe("https://app/cb?code=x&state=y");
    const [, init] = fetchImpl.mock.calls[0]!;
    expect(init.method).toBe("POST");
    expect(init.credentials).toBe("omit");
  });

  it("finalize throws on non-2xx", async () => {
    const fetchImpl = vi.fn().mockResolvedValue(new Response(null, { status: 409 }));
    await expect(finalize("code", undefined, fetchImpl as unknown as typeof fetch)).rejects.toThrow();
  });

  it("cancel POSTs without credentials", async () => {
    const fetchImpl = vi.fn().mockResolvedValue(new Response(null, { status: 200 }));
    await cancel("code", fetchImpl as unknown as typeof fetch);
    const [url, init] = fetchImpl.mock.calls[0]!;
    expect(String(url)).toContain("/oauth/wait/code/cancel");
    expect(init.method).toBe("POST");
    expect(init.credentials).toBe("omit");
  });
});
