import { describe, expect, it } from "vitest";
import {
  formatDateTime,
  formatPlatformRole,
  formatRelative,
  humanizeEnum,
  middleTruncate,
  toDate,
} from "./format";
import { pageTitle, titleHead } from "./head";
import { apiRequestSnippets } from "./snippets";

describe("toDate", () => {
  it("accepts millis, seconds, ISO strings and Dates", () => {
    const ms = Date.UTC(2026, 9, 18, 12, 0, 0);
    expect(toDate(ms)?.getTime()).toBe(ms);
    expect(toDate(ms / 1000)?.getTime()).toBe(ms);
    expect(toDate(new Date(ms).toISOString())?.getTime()).toBe(ms);
    expect(toDate(new Date(ms))?.getTime()).toBe(ms);
  });

  it("returns null for empty or invalid input", () => {
    expect(toDate(undefined)).toBeNull();
    expect(toDate(null)).toBeNull();
    expect(toDate(0)).toBeNull();
    expect(toDate("")).toBeNull();
    expect(toDate("not a date")).toBeNull();
  });
});

describe("formatDateTime", () => {
  it("uses the fallback for empty values", () => {
    expect(formatDateTime(undefined)).toBe("—");
    expect(formatDateTime(null, { fallback: "Never" })).toBe("Never");
  });
});

describe("formatRelative", () => {
  const now = Date.UTC(2026, 9, 18, 12, 0, 0);
  it.each([
    [now - 10_000, "just now"],
    [now - 3 * 60_000, "3 min ago"],
    [now - 2 * 3_600_000, "2 hr ago"],
    [now - 26 * 3_600_000, "yesterday"],
    [now - 3 * 86_400_000, "3 days ago"],
    [now + 5 * 60_000, "in 5 min"],
  ])("formats %s", (value, expected) => {
    expect(formatRelative(value, { now })).toBe(expected);
  });

  it("falls back to a date after a week", () => {
    expect(formatRelative(now - 30 * 86_400_000, { now })).not.toMatch(/ago/);
  });
});

describe("small formatters", () => {
  it("middle-truncates long IDs only", () => {
    expect(middleTruncate("short")).toBe("short");
    expect(middleTruncate("0123456789abcdefghijklmnop")).toBe("01234567…klmnop");
  });

  it("humanizes enums and roles", () => {
    expect(humanizeEnum("scan_qr_code")).toBe("Scan qr code");
    expect(formatPlatformRole("super_admin")).toBe("Platform admin");
    expect(formatPlatformRole("user")).toBe("Member");
  });
});

describe("pageTitle", () => {
  it("joins parts and appends the app name", () => {
    expect(pageTitle("API keys")).toBe("API keys · WA Gateway");
    expect(pageTitle("Chats", undefined, "Sales")).toBe("Chats · Sales · WA Gateway");
    expect(titleHead("Webhooks")).toEqual({ meta: [{ title: "Webhooks · WA Gateway" }] });
  });
});

describe("apiRequestSnippets", () => {
  it("builds curl / JS / Python for a GET", () => {
    const [curl, js, py] = apiRequestSnippets({
      url: "https://wa.example.com/api/v1/sessions",
      token: "wa_secret",
    });
    expect(curl?.code).toBe(
      "curl 'https://wa.example.com/api/v1/sessions' \\\n  -H 'Authorization: Bearer wa_secret'",
    );
    expect(js?.code).toContain('method: "GET"');
    expect(py?.code).toContain("requests.get(");
  });

  it("includes the JSON body for a POST", () => {
    const [curl, , py] = apiRequestSnippets({
      method: "POST",
      url: "https://x/api/v1/sessions/s1/messages",
      token: "t",
      body: { type: "text", text: "it's ok", preview: true },
    });
    expect(curl?.code).toContain("-X POST");
    expect(curl?.code).toContain(`-d '{"type":"text","text":"it'\\''s ok","preview":true}'`);
    expect(py?.code).toContain('"preview": True');
  });
});
