import { describe, it, expect } from "vitest";
import { parseNdjson, isPingFrame, isErrorFrame, isDataFrame } from "./ndjson";
import type { StreamFrame } from "./ndjson";

function streamFromChunks(chunks: string[]): ReadableStream<Uint8Array> {
  const enc = new TextEncoder();
  let i = 0;
  return new ReadableStream({
    pull(controller) {
      if (i < chunks.length) {
        controller.enqueue(enc.encode(chunks[i++]));
      } else {
        controller.close();
      }
    },
  });
}

async function collect(chunks: string[]): Promise<StreamFrame[]> {
  const ac = new AbortController();
  const out: StreamFrame[] = [];
  for await (const f of parseNdjson(streamFromChunks(chunks), ac.signal)) {
    out.push(f);
  }
  return out;
}

describe("parseNdjson", () => {
  it("parses one JSON object per line", async () => {
    const frames = await collect([
      '{"id":"a","event":"message","session":"s1"}\n',
      '{"id":"b","event":"message","session":"s1"}\n',
    ]);
    expect(frames).toHaveLength(2);
    expect((frames[0] as { id: string }).id).toBe("a");
    expect((frames[1] as { id: string }).id).toBe("b");
  });

  it("buffers across chunk boundaries", async () => {
    const frames = await collect(['{"id":"a","ev', 'ent":"x"}\n']);
    expect(frames).toHaveLength(1);
    expect((frames[0] as { id: string }).id).toBe("a");
  });

  it("handles \\r\\n line endings and blank lines", async () => {
    const frames = await collect(['{"id":"a"}\r\n', "\r\n", '{"id":"b"}\r\n']);
    expect(frames.map((f) => (f as { id: string }).id)).toEqual(["a", "b"]);
  });

  it("flushes a trailing line without newline at EOF", async () => {
    const frames = await collect(['{"id":"a"}\n', '{"id":"b"}']);
    expect(frames).toHaveLength(2);
  });

  it("skips malformed lines without throwing", async () => {
    const frames = await collect(["not json\n", '{"id":"ok"}\n']);
    expect(frames).toHaveLength(1);
    expect((frames[0] as { id: string }).id).toBe("ok");
  });

  it("classifies ping, error and data frames", async () => {
    const frames = await collect([
      '{"event":"ping"}\n',
      '{"event":"error","error":"replay failed"}\n',
      '{"id":"d","event":"message"}\n',
    ]);
    expect(frames[0] && isPingFrame(frames[0])).toBe(true);
    expect(frames[1] && isErrorFrame(frames[1])).toBe(true);
    expect(frames[2] && isDataFrame(frames[2])).toBe(true);
  });

  it("stops when the signal is aborted", async () => {
    const ac = new AbortController();
    ac.abort();
    const out: StreamFrame[] = [];
    for await (const f of parseNdjson(streamFromChunks(['{"id":"a"}\n']), ac.signal)) {
      out.push(f);
    }
    expect(out).toHaveLength(0);
  });
});
