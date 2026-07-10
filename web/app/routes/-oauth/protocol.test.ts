import { describe, it, expect } from "vitest";
import {
  isHeartbeat,
  isPending,
  isTerminal,
  parseBrowserCode,
  readNdjson,
  verificationMessage,
  waMeLink,
  type PendingSnapshot,
  type StatusFrame,
} from "./protocol";

/** Build a byte ReadableStream from raw NDJSON text, chunked at `chunkAt`
 * boundaries to exercise the line buffer across arbitrary byte splits. */
function ndjsonStream(text: string, chunkSize = 7): ReadableStream<Uint8Array> {
  const bytes = new TextEncoder().encode(text);
  let i = 0;
  return new ReadableStream<Uint8Array>({
    pull(controller) {
      if (i >= bytes.length) {
        controller.close();
        return;
      }
      controller.enqueue(bytes.slice(i, i + chunkSize));
      i += chunkSize;
    },
  });
}

const PENDING: PendingSnapshot = {
  status: "pending",
  app: { name: "Acme", logo: null },
  user_code: "483920",
  login_command: "login",
  target: { mode: "dm", number: "+6281234" },
  scopes: ["openid", "profile"],
  expires_at: Date.now() + 600_000,
};

describe("frame guards", () => {
  it("classifies pending / heartbeat / terminal", () => {
    expect(isPending(PENDING)).toBe(true);
    expect(isHeartbeat({ status: "heartbeat" })).toBe(true);
    expect(isTerminal({ status: "verified" })).toBe(true);
    expect(isTerminal({ status: "denied" })).toBe(true);
    expect(isTerminal({ status: "expired" })).toBe(true);
    expect(isTerminal({ status: "finalized" })).toBe(true);
    expect(isTerminal(PENDING)).toBe(false);
    expect(isHeartbeat(PENDING)).toBe(false);
  });
});

describe("parseBrowserCode", () => {
  it("reads the #c=<code> fragment", () => {
    expect(parseBrowserCode("#c=abc123")).toBe("abc123");
    expect(parseBrowserCode("c=abc123")).toBe("abc123");
  });
  it("accepts a bare fragment", () => {
    expect(parseBrowserCode("#XYZ")).toBe("XYZ");
  });
  it("returns null when absent or malformed", () => {
    expect(parseBrowserCode("")).toBeNull();
    expect(parseBrowserCode("#")).toBeNull();
    expect(parseBrowserCode("#other=1")).toBeNull();
  });
});

describe("readNdjson", () => {
  it("parses one object per line across arbitrary byte chunks", async () => {
    const text =
      JSON.stringify(PENDING) +
      "\n" +
      JSON.stringify({ status: "heartbeat" }) +
      "\n" +
      JSON.stringify({ status: "verified" }) +
      "\n";
    const frames: StatusFrame[] = [];
    await readNdjson(ndjsonStream(text, 5).getReader(), (f) => frames.push(f));
    expect(frames.map((f) => f.status)).toEqual(["pending", "heartbeat", "verified"]);
  });

  it("skips malformed lines and flushes a trailing newline-less frame", async () => {
    const text =
      "not json\n" +
      "{bad}\n" +
      JSON.stringify({ status: "expired" }); // no trailing newline
    const frames: StatusFrame[] = [];
    await readNdjson(ndjsonStream(text, 3).getReader(), (f) => frames.push(f));
    expect(frames.map((f) => f.status)).toEqual(["expired"]);
  });

  it("ignores blank lines", async () => {
    const text = "\n\n" + JSON.stringify({ status: "denied" }) + "\n\n";
    const frames: StatusFrame[] = [];
    await readNdjson(ndjsonStream(text, 4).getReader(), (f) => frames.push(f));
    expect(frames.map((f) => f.status)).toEqual(["denied"]);
  });
});

describe("waMeLink / verificationMessage", () => {
  it("builds the command string", () => {
    expect(verificationMessage("masuk", "483920")).toBe("masuk 483920");
  });
  it("strips non-digits and url-encodes the prefilled text", () => {
    const link = waMeLink("+62 812-3456", "login 483920");
    expect(link).toBe("https://wa.me/628123456?text=login%20483920");
  });
});
