// auth.code means "pairing code issued" when it carries a code and "pairing
// succeeded" when it carries a jid. Each meaning has its own cache effect.
import { beforeEach, describe, expect, it } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import { applyEvent, classifyAuthCode } from "./cacheBridge";
import { qk } from "../query";
import type { EventEnvelope } from "../api/types";

const SESSION = "sess_1";

function authCode(payload: Record<string, unknown>): EventEnvelope {
  return {
    schema: "v1",
    id: "evt_code",
    event: "auth.code",
    session: SESSION,
    organization: "org_1",
    timestamp: 1000,
    payload,
  } as EventEnvelope;
}

describe("classifyAuthCode", () => {
  it("reads a code as a pairing code being issued", () => {
    expect(classifyAuthCode({ code: "ABCD1234", phone: "628" })).toEqual({ kind: "pairing_code_issued", code: "ABCD1234" });
  });
  it("reads a jid as pairing success", () => {
    expect(classifyAuthCode({ jid: "628@s.whatsapp.net", lid: "1@lid" })).toEqual({ kind: "pair_succeeded", jid: "628@s.whatsapp.net" });
  });
  it("ignores a payload with neither", () => {
    expect(classifyAuthCode({ platform: "android" })).toEqual({ kind: "unknown" });
  });
});

describe("auth.code cache effects", () => {
  let qc: QueryClient;
  beforeEach(() => {
    qc = new QueryClient();
    qc.setQueryData(qk.session(SESSION), { id: SESSION, status: "scan_qr_code", updatedAt: 1 });
    qc.setQueryData(qk.sessionQR(SESSION), { code: "2@qr" });
  });

  it("an issued code is stored and leaves the QR and the row alone", () => {
    applyEvent(qc, authCode({ code: "ABCD1234", phone: "628" }));
    expect(qc.getQueryData(qk.sessionPairing(SESSION))).toEqual({ code: "ABCD1234" });
    expect(qc.getQueryData(qk.sessionQR(SESSION))).toEqual({ code: "2@qr" });
    expect(qc.getQueryState(qk.session(SESSION))?.isInvalidated).toBe(false);
  });

  it("success clears every artifact and refetches the session rows", () => {
    qc.setQueryData(qk.sessionPairing(SESSION), { code: "ABCD1234" });
    qc.setQueryData(qk.sessionPasskey(SESSION), { step: "confirm", code: "X", expiresAt: 1 });
    qc.setQueryData(qk.adminSessions(), { pages: [], pageParams: [] });
    applyEvent(qc, authCode({ jid: "628@s.whatsapp.net" }));
    expect(qc.getQueryData(qk.sessionQR(SESSION))).toBeUndefined();
    expect(qc.getQueryData(qk.sessionPairing(SESSION))).toBeUndefined();
    expect(qc.getQueryData(qk.sessionPasskey(SESSION))).toBeUndefined();
    expect(qc.getQueryState(qk.session(SESSION))?.isInvalidated).toBe(true);
    expect(qc.getQueryState(qk.adminSessions())?.isInvalidated).toBe(true);
  });

  it("an unrecognised payload changes nothing", () => {
    applyEvent(qc, authCode({}));
    expect(qc.getQueryData(qk.sessionQR(SESSION))).toEqual({ code: "2@qr" });
    expect(qc.getQueryState(qk.session(SESSION))?.isInvalidated).toBe(false);
  });
});
