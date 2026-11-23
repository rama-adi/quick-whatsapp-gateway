// Pairing artifacts (QR code, phone pairing code, passkey step) belong to one
// pairing attempt. Any status other than scan_qr_code ends the attempt, so the
// cache must not keep showing an old code after a restart, a failure or a link.
import { beforeEach, describe, expect, it } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import { applyEvent } from "./cacheBridge";
import { qk } from "../query";
import type { EventEnvelope, SessionStatus } from "../api/types";

const SESSION = "sess_1";

function evt(event: string, payload: Record<string, unknown>, timestamp = 1000): EventEnvelope {
  return {
    schema: "v1",
    id: `evt_${event}_${timestamp}`,
    event,
    session: SESSION,
    organization: "org_1",
    timestamp,
    payload,
  } as EventEnvelope;
}

function seedArtifacts(qc: QueryClient): void {
  qc.setQueryData(qk.sessionQR(SESSION), { code: "2@old" });
  qc.setQueryData(qk.sessionPairing(SESSION), { code: "ABCD1234" });
  qc.setQueryData(qk.sessionPasskey(SESSION), { step: "verify", expiresAt: 9 });
}

describe("pairing artifacts", () => {
  let qc: QueryClient;
  beforeEach(() => {
    qc = new QueryClient();
  });

  it.each<SessionStatus>(["starting", "working", "failed", "stopped", "logged_out"])(
    "%s clears the QR code, pairing code and passkey step",
    (status) => {
      seedArtifacts(qc);
      applyEvent(qc, evt("session.status", { status }));
      expect(qc.getQueryData(qk.sessionQR(SESSION))).toBeUndefined();
      expect(qc.getQueryData(qk.sessionPairing(SESSION))).toBeUndefined();
      expect(qc.getQueryData(qk.sessionPasskey(SESSION))).toBeUndefined();
    },
  );

  it("scan_qr_code keeps the current attempt's artifacts", () => {
    seedArtifacts(qc);
    applyEvent(qc, evt("session.status", { status: "scan_qr_code" }));
    expect(qc.getQueryData(qk.sessionQR(SESSION))).toEqual({ code: "2@old" });
    expect(qc.getQueryData(qk.sessionPairing(SESSION))).toEqual({ code: "ABCD1234" });
  });

  it("auth.qr records when the code expires when the event says how long it lives", () => {
    applyEvent(qc, evt("auth.qr", { code: "2@new", timeoutMs: 20_000 }, 5_000));
    expect(qc.getQueryData(qk.sessionQR(SESSION))).toEqual({ code: "2@new", expiresAt: 25_000 });
  });

  it("auth.qr without a timeout stores a code with no known expiry", () => {
    applyEvent(qc, evt("auth.qr", { code: "2@new" }));
    expect(qc.getQueryData(qk.sessionQR(SESSION))).toEqual({ code: "2@new" });
  });
});
