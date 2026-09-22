import { describe, expect, it } from "vitest";
import type { Message } from "~/lib/api/types";
import { parseExtras, parseMessage } from "./-viewer-ui";

const message: Message = {
  id: "row-1", waMessageId: "original", sessionId: "session", chatJid: "123@g.us",
  type: "text", body: "new text", direction: "in", fromMe: false,
  timestamp: 1, createdAt: 1, edited: true, deleted: false, hasMedia: false,
};

describe("message lifecycle display", () => {
  it("uses the persisted edited flag for plain text", () => {
    expect(parseMessage(message)).toEqual({ kind: "text", text: "new text" });
    expect(parseExtras(message).edited).toBe(true);
    expect(parseExtras({ ...message, edited: false, body: '{"edited":true}' }).edited).toBe(false);
  });

  it("hides deleted content regardless of its original message type", () => {
    for (const type of ["text", "poll", "image", "system"]) {
      expect(parseMessage({ ...message, type, deleted: true })).toEqual({ kind: "deleted" });
    }
  });
});
