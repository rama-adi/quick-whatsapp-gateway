import { describe, it, expect } from "vitest";
import {
  isConnectedFrame,
  isPingFrame,
  isErrorFrame,
  isDataFrame,
} from "./frames";
import type { StreamFrame } from "./frames";

describe("frame guards", () => {
  it("classifies connected, ping, error and data frames", () => {
    const connected: StreamFrame = { event: "connected", heartbeatSeconds: 20 };
    const ping: StreamFrame = { event: "ping" };
    const error: StreamFrame = { event: "error", error: "replay failed" };
    const data = { id: "d", event: "message" } as unknown as StreamFrame;

    expect(isConnectedFrame(connected)).toBe(true);
    // The connected frame must NOT be mistaken for a ping or a data frame.
    expect(isPingFrame(connected)).toBe(false);
    expect(isDataFrame(connected)).toBe(false);

    expect(isPingFrame(ping)).toBe(true);
    expect(isErrorFrame(error)).toBe(true);
    expect(isDataFrame(data)).toBe(true);
  });
});
