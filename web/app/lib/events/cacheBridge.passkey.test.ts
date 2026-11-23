import { describe, it, expect, beforeEach } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import { applyEvent } from "./cacheBridge";
import { qk } from "../query";
import type { EventEnvelope } from "../api/types";

const SESSION = "sess_1";

function evt(event: string, payload: Record<string, unknown>): EventEnvelope {
  return {
    schema: "v1",
    id: "evt_x",
    event,
    session: SESSION,
    organization: "org_1",
    timestamp: 1000,
    payload,
  } as EventEnvelope;
}

describe("passkey and failure reasons", () => {
  let qc: QueryClient;
  beforeEach(() => {
    qc = new QueryClient();
  });

  it("auth.passkey stores the step until the pairing leaves scan_qr_code", () => {
    const step = { step: "verify", publicKey: { challenge: "abc", rpId: "whatsapp.com" }, expiresAt: 5000 };
    applyEvent(qc, evt("auth.passkey", step));
    expect(qc.getQueryData(qk.sessionPasskey(SESSION))).toEqual(step);

    applyEvent(qc, evt("session.status", { status: "scan_qr_code" }));
    expect(qc.getQueryData(qk.sessionPasskey(SESSION))).toEqual(step);

    applyEvent(qc, evt("session.status", { status: "failed", failureReason: "passkey_failed" }));
    expect(qc.getQueryData(qk.sessionPasskey(SESSION))).toBeUndefined();
    expect(qc.getQueryData(qk.sessionFailure(SESSION))).toBe("passkey_failed");
  });

  it("a non-failed status clears the failure reason", () => {
    applyEvent(qc, evt("session.status", { status: "failed", failureReason: "qr_timeout" }));
    applyEvent(qc, evt("session.status", { status: "scan_qr_code" }));
    expect(qc.getQueryData(qk.sessionFailure(SESSION))).toBeUndefined();
  });

  it("ignores auth.passkey events with an unknown step", () => {
    applyEvent(qc, evt("auth.passkey", { step: "other", expiresAt: 1 }));
    expect(qc.getQueryData(qk.sessionPasskey(SESSION))).toBeUndefined();
  });
});
