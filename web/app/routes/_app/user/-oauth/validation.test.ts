import { describe, it, expect } from "vitest";
import {
  validateLoginCommand,
  validateRedirectUri,
  validateRedirectUris,
  normalizeRedirectUris,
  clamp,
  humanizeSeconds,
  buildAuthorizeUrl,
  discoveryUrl,
} from "./validation";
import { nodeQuickstart } from "./quickstart";

describe("validateLoginCommand", () => {
  it("accepts valid single-word commands", () => {
    expect(validateLoginCommand("login")).toBeNull();
    expect(validateLoginCommand("masuk")).toBeNull();
    expect(validateLoginCommand("sign-in_2")).toBeNull();
    expect(validateLoginCommand("ab")).toBeNull();
    expect(validateLoginCommand("a".repeat(32))).toBeNull();
  });
  it("rejects uppercase, spaces, length, and bad chars", () => {
    expect(validateLoginCommand("Login")).toMatch(/lowercase/i);
    expect(validateLoginCommand("log in")).toMatch(/single word/i);
    expect(validateLoginCommand("a")).toMatch(/2 char/i);
    expect(validateLoginCommand("a".repeat(33))).toMatch(/32 char/i);
    expect(validateLoginCommand("log!n")).toMatch(/lowercase letters/i);
    expect(validateLoginCommand("")).toMatch(/enter/i);
  });
});

describe("validateRedirectUri", () => {
  it("accepts absolute https", () => {
    expect(validateRedirectUri("https://app.example.com/callback")).toBeNull();
  });
  it("accepts http only for localhost / loopback", () => {
    expect(validateRedirectUri("http://localhost:3000/cb")).toBeNull();
    expect(validateRedirectUri("http://127.0.0.1:3000/cb")).toBeNull();
    expect(validateRedirectUri("http://example.com/cb")).toMatch(/localhost/i);
  });
  it("rejects fragments", () => {
    expect(validateRedirectUri("https://app.example.com/cb#x")).toMatch(
      /fragment/i,
    );
  });
  it("rejects non-http(s) and relative", () => {
    expect(validateRedirectUri("ftp://x/y")).toMatch(/http/i);
    expect(validateRedirectUri("/callback")).toMatch(/valid absolute/i);
  });
});

describe("validateRedirectUris", () => {
  it("flags duplicates and requires at least one valid", () => {
    const dup = validateRedirectUris([
      "https://a.com/cb",
      "https://a.com/cb",
    ]);
    expect(dup.duplicates.has(1)).toBe(true);
    expect(dup.ok).toBe(false);

    const empty = validateRedirectUris([""]);
    expect(empty.ok).toBe(false);

    const good = validateRedirectUris(["https://a.com/cb", ""]);
    expect(good.ok).toBe(true);
    expect(good.perUri[1]).toBeNull();
  });
});

describe("normalizeRedirectUris", () => {
  it("trims, drops empties, de-dupes, preserves order", () => {
    expect(
      normalizeRedirectUris([
        " https://a.com/cb ",
        "",
        "https://b.com/cb",
        "https://a.com/cb",
      ]),
    ).toEqual(["https://a.com/cb", "https://b.com/cb"]);
  });
});

describe("clamp / humanizeSeconds", () => {
  it("clamps into range and rounds", () => {
    expect(clamp(10, 60, 900)).toBe(60);
    expect(clamp(99999, 60, 900)).toBe(900);
    expect(clamp(120.6, 60, 900)).toBe(121);
    expect(clamp(NaN, 60, 900)).toBe(60);
  });
  it("humanizes common durations", () => {
    expect(humanizeSeconds(900)).toBe("15 minutes");
    expect(humanizeSeconds(2_592_000)).toBe("30 days");
    expect(humanizeSeconds(3_600)).toBe("1 hour");
    expect(humanizeSeconds(1)).toBe("1 second");
  });
});

describe("buildAuthorizeUrl", () => {
  it("builds a spec-shaped authorize URL with PKCE placeholders", () => {
    const url = buildAuthorizeUrl({
      issuer: "https://gw.example.com/",
      clientId: "wa_abc",
      redirectUri: "https://app.example.com/callback",
      scopes: ["openid", "profile"],
    });
    const parsed = new URL(url);
    expect(parsed.origin + parsed.pathname).toBe(
      "https://gw.example.com/oauth/authorize",
    );
    expect(parsed.searchParams.get("response_type")).toBe("code");
    expect(parsed.searchParams.get("client_id")).toBe("wa_abc");
    expect(parsed.searchParams.get("code_challenge_method")).toBe("S256");
    expect(parsed.searchParams.get("scope")).toBe("openid profile");
    expect(parsed.searchParams.get("acr_values")).toBeNull();
  });
  it("includes acr_values when provided", () => {
    const url = buildAuthorizeUrl({
      issuer: "https://gw.example.com",
      clientId: "wa_abc",
      redirectUri: "https://app.example.com/cb",
      scopes: ["openid"],
      acrValues: "wa:group",
    });
    expect(new URL(url).searchParams.get("acr_values")).toBe("wa:group");
  });
});

describe("discoveryUrl", () => {
  it("appends the well-known path once", () => {
    expect(discoveryUrl("https://gw.example.com/")).toBe(
      "https://gw.example.com/.well-known/openid-configuration",
    );
  });
});

describe("nodeQuickstart", () => {
  it("inlines discovery URL, client_id, redirect and scopes", () => {
    const snippet = nodeQuickstart({
      issuer: "https://gw.example.com",
      clientId: "wa_abc",
      clientType: "confidential",
      redirectUri: "https://app.example.com/callback",
      scopes: ["openid", "phone"],
    });
    expect(snippet).toContain(
      "https://gw.example.com/.well-known/openid-configuration",
    );
    expect(snippet).toContain('"wa_abc"');
    expect(snippet).toContain('"https://app.example.com/callback"');
    expect(snippet).toContain('"openid phone"');
    expect(snippet).toContain("WA_CLIENT_SECRET");
  });
  it("uses PKCE-only auth for public clients (no secret)", () => {
    const snippet = nodeQuickstart({
      issuer: "https://gw.example.com",
      clientId: "wa_pub",
      clientType: "public",
      redirectUri: "https://app.example.com/cb",
      scopes: ["openid"],
    });
    expect(snippet).toContain('token_endpoint_auth_method: "none"');
    expect(snippet).not.toContain("WA_CLIENT_SECRET");
  });
});
