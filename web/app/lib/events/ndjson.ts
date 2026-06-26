// Pure NDJSON frame parser for the event stream.
// FROZEN — owned by the foundation agent. Pure + unit-tested (ndjson.test.ts).

import type { EventEnvelope } from "../api/types";

/** Periodic heartbeat sent ~every 20s. No id/payload; resets the watchdog. */
export type PingFrame = { event: "ping" };

/** In-band error line emitted after headers flush (e.g. since-replay failed). */
export type ErrorFrame = { event: "error"; error: string };

/** One decoded line: a data envelope, a ping, or an in-band error. */
export type StreamFrame = EventEnvelope | PingFrame | ErrorFrame;

export function isPingFrame(f: StreamFrame): f is PingFrame {
  return (f as PingFrame).event === "ping" && !("id" in f);
}

export function isErrorFrame(f: StreamFrame): f is ErrorFrame {
  return (f as ErrorFrame).event === "error" && typeof (f as ErrorFrame).error === "string";
}

export function isDataFrame(f: StreamFrame): f is EventEnvelope {
  return !isPingFrame(f) && !isErrorFrame(f) && typeof (f as EventEnvelope).id === "string";
}

/**
 * Stream a ReadableStream of bytes as decoded NDJSON frames. Buffers across
 * chunk boundaries, tolerates \r\n, skips blank lines, and ignores malformed
 * lines rather than throwing (the transport decides what to do on EOF/abort).
 */
export async function* parseNdjson(
  body: ReadableStream<Uint8Array>,
  signal: AbortSignal,
): AsyncGenerator<StreamFrame> {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";

  const onAbort = (): void => {
    void reader.cancel().catch(() => {});
  };
  signal.addEventListener("abort", onAbort, { once: true });

  try {
    while (!signal.aborted) {
      const { done, value } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });

      let nl: number;
      while ((nl = buffer.indexOf("\n")) >= 0) {
        const line = buffer.slice(0, nl).trim();
        buffer = buffer.slice(nl + 1);
        if (line === "") continue;
        const frame = tryParse(line);
        if (frame) yield frame;
      }
    }
    // Flush any trailing complete line at clean EOF.
    const tail = buffer.trim();
    if (tail !== "") {
      const frame = tryParse(tail);
      if (frame) yield frame;
    }
  } finally {
    signal.removeEventListener("abort", onAbort);
    reader.releaseLock();
  }
}

function tryParse(line: string): StreamFrame | null {
  try {
    const obj = JSON.parse(line) as unknown;
    if (obj && typeof obj === "object") {
      return obj as StreamFrame;
    }
    return null;
  } catch {
    return null;
  }
}
