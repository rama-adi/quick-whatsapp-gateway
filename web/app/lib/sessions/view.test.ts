import { describe, expect, it } from "vitest";
import type { WASession } from "../api/types";
import {
  LINKING_STALL_MS,
  deriveSessionView,
  isAwaitingPairing,
  nextViewDeadline,
  type SessionViewInput,
} from "./view";
import { SETTLE_MS, settlingAction } from "./useSessionView";

function row(over: Partial<WASession> = {}): WASession {
  return {
    id: "s1",
    organizationId: "o1",
    gatewayId: "g1",
    status: "stopped",
    isAdminSession: false,
    autoRead: false,
    presenceTyping: false,
    ratePerMin: 0,
    ratePerHour: 0,
    historySyncDays: 0,
    createdAt: 1,
    updatedAt: 100,
    ...over,
  };
}

function input(over: Partial<SessionViewInput> = {}): SessionViewInput {
  return {
    session: row(),
    loading: false,
    error: undefined,
    qr: undefined,
    pairingCode: undefined,
    passkey: undefined,
    failure: undefined,
    linkingSince: undefined,
    now: 1_000,
    ...over,
  };
}

describe("deriveSessionView", () => {
  it("is loading, then unreachable when the row fails to load", () => {
    expect(deriveSessionView(input({ session: undefined, loading: true })).kind).toBe("loading");
    expect(deriveSessionView(input({ session: undefined, error: new Error("x") })).kind).toBe("unreachable");
  });

  it("keeps a cached row when a refetch fails", () => {
    const view = deriveSessionView(input({ session: row({ status: "working", waJid: "j" }), error: new Error("x") }));
    expect(view.kind).toBe("working");
  });

  it("maps lifecycle statuses", () => {
    expect(deriveSessionView(input({ session: row({ status: "stopped", waJid: "j" }) }))).toEqual({ kind: "stopped", paired: true });
    expect(deriveSessionView(input({ session: row({ status: "starting" }) }))).toEqual({ kind: "connecting", paired: false });
    expect(deriveSessionView(input({ session: row({ status: "failed" }), failure: "qr_timeout" }))).toEqual({ kind: "failed", reason: "qr_timeout" });
    expect(deriveSessionView(input({ session: row({ status: "logged_out" }) })).kind).toBe("logged_out");
    expect(deriveSessionView(input({ session: row({ status: "working", waJid: "j" }) })).kind).toBe("working");
  });

  it("awaits pairing until a live code exists", () => {
    const awaiting = row({ status: "scan_qr_code" });
    expect(deriveSessionView(input({ session: awaiting })).kind).toBe("awaiting_pairing");
    expect(deriveSessionView(input({ session: awaiting, qr: { code: "2@a" } }))).toMatchObject({ kind: "qr", code: "2@a" });
  });

  it("drops an expired QR code", () => {
    const awaiting = row({ status: "scan_qr_code" });
    const qr = { code: "2@a", expiresAt: 2_000 };
    expect(deriveSessionView(input({ session: awaiting, qr, now: 1_999 })).kind).toBe("qr");
    expect(deriveSessionView(input({ session: awaiting, qr, now: 2_000 })).kind).toBe("awaiting_pairing");
  });

  it("prefers a requested phone code, and a passkey step over both", () => {
    const awaiting = row({ status: "scan_qr_code" });
    const both = { session: awaiting, qr: { code: "2@a" }, pairingCode: { code: "ABCD1234" } };
    const view = deriveSessionView(input(both));
    expect(view).toMatchObject({ kind: "pairing_code", code: "ABCD1234", artifacts: { qr: { code: "2@a" } } });
    const passkey = { step: "verify" as const, expiresAt: 9 };
    expect(deriveSessionView(input({ ...both, passkey })).kind).toBe("passkey");
    expect(isAwaitingPairing(view)).toBe(true);
  });

  it("ignores artifacts outside scan_qr_code", () => {
    const view = deriveSessionView(input({ session: row({ status: "starting" }), qr: { code: "2@old" } }));
    expect(view.kind).toBe("connecting");
  });

  it("bounds linking: working without an identity stalls after LINKING_STALL_MS", () => {
    const linking = row({ status: "working" });
    expect(deriveSessionView(input({ session: linking, linkingSince: 0, now: LINKING_STALL_MS - 1 }))).toEqual({ kind: "linking", stalled: false });
    expect(deriveSessionView(input({ session: linking, linkingSince: 0, now: LINKING_STALL_MS }))).toEqual({ kind: "linking", stalled: true });
  });
});

describe("nextViewDeadline", () => {
  it("is the earliest artifact expiry or linking stall still ahead", () => {
    const awaiting = row({ status: "scan_qr_code" });
    expect(nextViewDeadline(input({ session: awaiting, qr: { code: "q", expiresAt: 5_000 }, pairingCode: { code: "c", expiresAt: 3_000 } }))).toBe(3_000);
    expect(nextViewDeadline(input({ session: row({ status: "working" }), linkingSince: 0 }))).toBe(LINKING_STALL_MS);
    expect(nextViewDeadline(input())).toBeUndefined();
  });
});

describe("settlingAction", () => {
  const answered = (action: "start" | "stop" | "logout", data: WASession, submittedAt = 0) => ({ action, data, submittedAt });

  it("keeps a :start overlay while the row still shows stopped", () => {
    const stopped = row({ status: "stopped", updatedAt: 100 });
    expect(settlingAction(stopped, answered("start", stopped), 1_000)).toBe("start");
  });

  it("ends when the effect shows, a newer row arrives, or time runs out", () => {
    const stopped = row({ status: "stopped", updatedAt: 100 });
    expect(settlingAction(row({ status: "starting", updatedAt: 100 }), answered("start", stopped), 1_000)).toBeUndefined();
    expect(settlingAction(row({ status: "failed", updatedAt: 200 }), answered("start", stopped), 1_000)).toBeUndefined();
    expect(settlingAction(stopped, answered("start", stopped), SETTLE_MS)).toBeUndefined();
  });

  it("does not hold an action whose response already shows its effect", () => {
    const out = row({ status: "logged_out", updatedAt: 300 });
    expect(settlingAction(out, answered("logout", out), 1_000)).toBeUndefined();
  });
});
