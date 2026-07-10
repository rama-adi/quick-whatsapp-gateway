import { describe, it, expect } from "vitest";
import { describeScopes } from "./scopes";
import { formatCountdown } from "./useCountdown";

describe("describeScopes", () => {
  it("maps known scopes to plain-language lines", () => {
    const lines = describeScopes(["openid", "phone", "wa:group"]);
    expect(lines.map((l) => l.key)).toEqual(["openid", "phone", "wa:group"]);
    expect(lines[0]!.label).toMatch(/it's you/i);
    expect(lines[1]!.description).toMatch(/phone number/i);
  });
  it("falls back to the raw token for unknown scopes", () => {
    const [line] = describeScopes(["custom:thing"]);
    expect(line!.label).toBe("custom:thing");
    expect(line!.description).toBeTruthy();
  });
  it("localizes known scopes and the unknown fallback to Indonesian", () => {
    const lines = describeScopes(["phone", "custom:thing"], "id");
    expect(lines[0]!.description).toMatch(/nomor telepon/i);
    expect(lines[1]!.label).toBe("custom:thing");
    expect(lines[1]!.description).toMatch(/akses tambahan/i);
  });
});

describe("formatCountdown", () => {
  it("formats as m:ss and rounds up", () => {
    expect(formatCountdown(600_000)).toBe("10:00");
    expect(formatCountdown(65_000)).toBe("1:05");
    expect(formatCountdown(1_500)).toBe("0:02");
    expect(formatCountdown(0)).toBe("0:00");
  });
});
