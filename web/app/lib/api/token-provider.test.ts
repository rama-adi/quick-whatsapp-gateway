import { beforeEach, describe, expect, it, vi } from "vitest";

const { mintGatewayToken } = vi.hoisted(() => ({
  mintGatewayToken: vi.fn<() => Promise<{ token: string }>>(),
}));

vi.mock("~/lib/auth/token", () => ({ mintGatewayToken }));

import {
  clearGatewayToken,
  getGatewayToken,
  onTokenRefresh,
} from "./token-provider";

describe("gateway token identity transitions", () => {
  beforeEach(() => {
    mintGatewayToken.mockReset();
    clearGatewayToken();
  });

  it("does not reuse a cached token after sign-out or organization change", async () => {
    mintGatewayToken
      .mockResolvedValueOnce({ token: "first-identity" })
      .mockResolvedValueOnce({ token: "second-identity" });

    expect(await getGatewayToken()).toBe("first-identity");
    expect(await getGatewayToken()).toBe("first-identity");
    expect(mintGatewayToken).toHaveBeenCalledTimes(1);

    clearGatewayToken();

    expect(await getGatewayToken()).toBe("second-identity");
    expect(mintGatewayToken).toHaveBeenCalledTimes(2);
  });

  it("notifies the realtime owner when an identity token is cleared", () => {
    const listener = vi.fn();
    const unsubscribe = onTokenRefresh(listener);

    clearGatewayToken();

    expect(listener).toHaveBeenCalledWith(null);
    unsubscribe();
  });

  it("cannot restore an invalidated token from an older in-flight mint", async () => {
    let resolveOld!: (value: { token: string }) => void;
    const oldMint = new Promise<{ token: string }>((resolve) => {
      resolveOld = resolve;
    });
    mintGatewayToken
      .mockReturnValueOnce(oldMint)
      .mockResolvedValueOnce({ token: "new-identity" });

    const oldRequest = getGatewayToken();
    clearGatewayToken();
    const newRequest = getGatewayToken();
    resolveOld({ token: "old-identity" });

    await expect(oldRequest).resolves.toBeNull();
    await expect(newRequest).resolves.toBe("new-identity");
    await expect(getGatewayToken()).resolves.toBe("new-identity");
  });
});
