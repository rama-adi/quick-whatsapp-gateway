import { describe, it, expect } from "vitest";
import { passkeyScript } from "./passkey-panel";

describe("passkeyScript", () => {
  it("is valid JavaScript that embeds the WebAuthn options", () => {
    const options = { challenge: "Y2hhbGxlbmdl", rpId: "whatsapp.com", allowCredentials: [{ id: "aWQ", type: "public-key" }] };
    const script = passkeyScript(options);
    // Parses without running (the script touches document and navigator).
    expect(() => new Function(script)).not.toThrow();
    expect(script).toContain(JSON.stringify(options));
  });
});
