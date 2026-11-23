// Recovery after missed events: every new socket refetches session-scoped
// queries (not chat history), and a resync frame is routed to its handler.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import { isSessionStateKey, resyncSessionState } from "./cacheBridge";
import { isDataFrame, isResyncFrame, type StreamFrame } from "./frames";
import { qk } from "../query";

vi.mock("../api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../api/client")>();
  return {
    ...actual,
    apiUrl: (path: string) => `http://api.test/api/v1${path}`,
    fetchJSON: vi.fn(async () => ({ ticket: "t1", expiresInSeconds: 30, url: "" })),
  };
});

describe("resyncSessionState", () => {
  it("invalidates session rows, QR and identity but not chat history", () => {
    const qc = new QueryClient();
    const keys = {
      list: qk.sessions(),
      detail: qk.session("s1"),
      qr: qk.sessionQR("s1"),
      me: qk.sessionMe("s1"),
      admin: qk.adminSessions(),
      chats: qk.chats("s1"),
      messages: qk.chatMessages("s1", "c1"),
      contacts: qk.contacts("s1", {}),
      backfill: ["admin", "sessions", "s1", "backfill"],
    };
    for (const key of Object.values(keys)) qc.setQueryData(key, { seeded: true });

    resyncSessionState(qc);

    const invalidated = (key: readonly unknown[]) => qc.getQueryState(key)?.isInvalidated;
    expect(invalidated(keys.list)).toBe(true);
    expect(invalidated(keys.detail)).toBe(true);
    expect(invalidated(keys.qr)).toBe(true);
    expect(invalidated(keys.me)).toBe(true);
    expect(invalidated(keys.admin)).toBe(true);
    expect(invalidated(keys.chats)).toBe(false);
    expect(invalidated(keys.messages)).toBe(false);
    expect(invalidated(keys.contacts)).toBe(false);
    expect(invalidated(keys.backfill)).toBe(false);
  });

  it("classifies session-state keys", () => {
    expect(isSessionStateKey(["sessions", "s1", "pairing"])).toBe(false);
    expect(isSessionStateKey(["presence", "s1", "j"])).toBe(false);
  });
});

describe("resync frame", () => {
  it("is not a data frame", () => {
    const frame: StreamFrame = { event: "resync", reason: "replay_truncated" };
    expect(isResyncFrame(frame)).toBe(true);
    expect(isDataFrame(frame)).toBe(false);
  });
});

class FakeSocket {
  static last: FakeSocket | undefined;
  onmessage: ((ev: { data: string }) => void) | null = null;
  onerror: (() => void) | null = null;
  onclose: (() => void) | null = null;
  constructor(public url: string) {
    FakeSocket.last = this;
  }
  close(): void {
    this.onclose?.();
  }
  emit(frame: unknown): void {
    this.onmessage?.({ data: JSON.stringify(frame) });
  }
}

describe("openEventStream", () => {
  beforeEach(() => {
    vi.stubGlobal("WebSocket", FakeSocket);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("reports connected and resync frames to their own handlers", async () => {
    const { openEventStream } = await import("./stream");
    const ac = new AbortController();
    const onConnected = vi.fn();
    const onResync = vi.fn();
    const onPing = vi.fn();
    const onEvent = vi.fn();
    const done = openEventStream({
      signal: ac.signal,
      onEvent,
      onPing,
      onConnected,
      onResync,
      onError: () => {},
    });
    await vi.waitFor(() => expect(FakeSocket.last).toBeDefined());
    const socket = FakeSocket.last!;

    socket.emit({ event: "connected", heartbeatSeconds: 20 });
    socket.emit({ event: "ping" });
    socket.emit({ event: "resync", reason: "replay_truncated" });

    expect(onConnected).toHaveBeenCalledTimes(1);
    expect(onPing).toHaveBeenCalledTimes(2);
    expect(onResync).toHaveBeenCalledTimes(1);
    expect(onEvent).not.toHaveBeenCalled();
    ac.abort();
    await done;
  });
});
