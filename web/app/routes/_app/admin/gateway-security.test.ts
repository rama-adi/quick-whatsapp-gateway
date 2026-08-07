import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const here = fileURLToPath(new URL(".", import.meta.url));
const enrollment = readFileSync(`${here}-gateway-enrollment.tsx`, "utf8");
const hooks = readFileSync(`${here}../../../lib/api/hooks/admin.ts`, "utf8");
const gate = readFileSync(`${here}route.tsx`, "utf8");
const listRoute = readFileSync(`${here}gateways.tsx`, "utf8");
const detailRoute = readFileSync(`${here}gateways.$gatewayId.tsx`, "utf8");

describe("gateway administration security boundaries", () => {
  it("keeps the plaintext enrollment response out of persistent storage and query cache", () => {
    expect(enrollment).not.toMatch(/localStorage|sessionStorage|URLSearchParams|console\./);
    expect(listRoute).not.toMatch(/(?:search|navigate)\([^\n]*token/i);
    expect(detailRoute).not.toMatch(/(?:search|navigate)\([^\n]*token/i);
    expect(hooks).toContain("must remain in the component that received it");
    expect(hooks).not.toMatch(/setQueryData\([^\n]*Enrollment/);
  });

  it("uses the gateway enrollment environment variable in the one-time configuration", () => {
    expect(enrollment).toContain("GATEWAY_ENROLLMENT_TOKEN=${result.token}");
    expect(enrollment).not.toContain("\\nENROLLMENT_TOKEN=${result.token}");
  });

  it("inherits the super_admin route authorization gate", () => {
    expect(gate).toContain('requireRole(context.session, "super_admin")');
  });
});
