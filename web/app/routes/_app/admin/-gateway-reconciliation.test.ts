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
