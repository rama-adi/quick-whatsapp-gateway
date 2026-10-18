import { describe, expect, it } from "vitest";
import { keystoreBytes, keystorePresence, reconciliationSubject, revisionGap } from "./-gateway-reconciliation";

describe("gateway reconciliation rendering", () => {
  it("shows the desired/applied revision gap and keystore health without exposing a secret", () => {
    expect(revisionGap(3, 5)).toBe("3 applied / 5 desired (2 revisions behind)");
    expect(keystorePresence({ keystorePresent: true } as never)).toBe("Present");
    expect(keystorePresence({ keystorePresent: false } as never)).toBe("Not present");
    expect(keystoreBytes({ keystoreBytes: 1536 } as never)).toBe("1.5 KiB");
  });

  it("renders unexpected local devices as unassigned without inventing a session or organization", () => {
    expect(reconciliationSubject({ status: "unexpected_local_device", deviceJid: "device@lid", sessionId: "untrusted-session" } as never)).toBe("Unassigned local device");
    expect(reconciliationSubject({ status: "applied", deviceJid: "device@lid", sessionId: "ses_1" } as never)).toBe("Session ses_1");
  });
});

describe("gateway lifecycle and capabilities", () => {
  it("offers only the transitions the API accepts", async () => {
    const { availableActions } = await import("./-gateway-reconciliation");
    const list = (status: string, enrolledAt?: number) => [...availableActions({ status, enrolledAt } as never)].sort();
    expect(list("pending_enrollment")).toEqual(["disable", "replaceToken"]);
    expect(list("active", 1)).toEqual(["disable", "drain"]);
    expect(list("draining", 1)).toEqual(["disable", "resume"]);
    expect(list("drained", 1)).toEqual(["disable", "reenroll", "resume"]);
    expect(list("disabled", 1)).toEqual(["reenable", "reenroll"]);
    expect(list("disabled")).toEqual(["reenable"]);
  });

  it("renders capabilities as readable labels instead of raw JSON", async () => {
    const { capabilityLabels } = await import("./-gateway-reconciliation");
    expect(capabilityLabels(null)).toEqual([]);
    expect(capabilityLabels(["GATEWAY_CAPABILITY_EVENT_STREAM", 1, "GATEWAY_CAPABILITY_UNKNOWN"])).toEqual(["Event stream", "Session engine"]);
    expect(capabilityLabels({ commandExecution: true, eventStream: false, maxSessions: 50 })).toEqual(["Command execution", "Max sessions: 50"]);
  });
});
