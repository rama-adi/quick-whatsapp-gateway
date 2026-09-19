import { describe, expect, it } from "vitest";
import { getQueryClient } from "./query";

describe("getQueryClient", () => {
  it("isolates caches between server requests", () => {
    const firstRequest = getQueryClient();
    const secondRequest = getQueryClient();

    firstRequest.setQueryData(["sessions"], { organization: "org-a" });

    expect(secondRequest).not.toBe(firstRequest);
    expect(secondRequest.getQueryData(["sessions"])).toBeUndefined();
  });
});
