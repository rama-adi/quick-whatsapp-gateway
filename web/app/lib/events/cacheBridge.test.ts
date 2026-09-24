import { describe, it, expect, beforeEach } from "vitest";
import { QueryClient, type InfiniteData } from "@tanstack/react-query";
import { applyEvent } from "./cacheBridge";
import { qk } from "../query";
import type { Page } from "../api/envelope";
import type { Chat, EventEnvelope, Message, WASession } from "../api/types";

const SESSION = "sess_1";

function evt(event: string, payload: Record<string, unknown>, id = "evt_x"): EventEnvelope {
  return {
    schema: "v1",
    id,
    event,
    session: SESSION,
    organization: "org_1",
    timestamp: 1000,
    payload,
  } as EventEnvelope;
}

function infinite<T>(items: T[]): InfiniteData<Page<T>, string | undefined> {
  return { pageParams: [undefined], pages: [{ data: items, nextCursor: null }] };
}

describe("applyEvent", () => {
  let qc: QueryClient;
  beforeEach(() => {
    qc = new QueryClient();
  });

  it("session.status patches the single session + list rows", () => {
    const base: WASession = {
      id: SESSION,
      organizationId: "org_1",
      gatewayId: "gw_1",
      status: "starting",
      isAdminSession: false,
      autoRead: false,
      presenceTyping: false,
      ratePerMin: 10,
      ratePerHour: 100,
      createdAt: 1,
      updatedAt: 1,
    };
    qc.setQueryData(qk.session(SESSION), base);
    qc.setQueryData(qk.sessions(), infinite([base]));

    applyEvent(qc, evt("session.status", { status: "working" }));

    expect(qc.getQueryData<WASession>(qk.session(SESSION))?.status).toBe("working");
    const list = qc.getQueryData<InfiniteData<Page<WASession>>>(qk.sessions());
    expect(list?.pages[0]?.data[0]?.status).toBe("working");
  });

  it("logged_out clears cached attachment and stale pairing artifacts", () => {
    const paired: WASession = {
      id: SESSION,
      organizationId: "org_1",
      gatewayId: "gw_1",
      status: "working",
      waJid: "628111@s.whatsapp.net",
      waLid: "777@lid",
      phoneNumber: "628111",
      isAdminSession: false,
      autoRead: false,
      presenceTyping: false,
      ratePerMin: 10,
      ratePerHour: 100,
      createdAt: 1,
      updatedAt: 1,
    };
    qc.setQueryData(qk.session(SESSION), paired);
    qc.setQueryData(qk.sessions(), infinite([paired]));
    qc.setQueryData(qk.sessionQR(SESSION), { code: "stale-qr" });
    qc.setQueryData(qk.sessionPairing(SESSION), { code: "STALE" });

    applyEvent(qc, evt("session.status", { status: "logged_out" }));

    const session = qc.getQueryData<WASession>(qk.session(SESSION));
    expect(session).toMatchObject({ status: "logged_out" });
    expect(session?.waJid).toBeUndefined();
    expect(session?.waLid).toBeUndefined();
    expect(session?.phoneNumber).toBeUndefined();
    expect(qc.getQueryData(qk.sessionQR(SESSION))).toBeUndefined();
    expect(qc.getQueryData(qk.sessionPairing(SESSION))).toBeUndefined();
  });

  it("auth.qr seeds the live QR query", () => {
    applyEvent(qc, evt("auth.qr", { code: "2@abc" }));
    expect(qc.getQueryData(qk.sessionQR(SESSION))).toEqual({ code: "2@abc" });
  });

  it("auth.code clears pairing artifacts and refreshes attached identity", () => {
    qc.setQueryData(qk.sessionQR(SESSION), { code: "stale" });
    qc.setQueryData(qk.sessionPairing(SESSION), { code: "1234" });
    qc.setQueryData(qk.session(SESSION), { id: SESSION });

    applyEvent(qc, evt("auth.code", { jid: "6281@s.whatsapp.net", lid: "x@lid" }));

    expect(qc.getQueryData(qk.sessionQR(SESSION))).toBeUndefined();
    expect(qc.getQueryData(qk.sessionPairing(SESSION))).toBeUndefined();
    expect(qc.getQueryState(qk.session(SESSION))?.isInvalidated).toBe(true);
  });

  it("message prepends to page 0 and bumps the chat", () => {
    const chatJid = "123@s.whatsapp.net";
    qc.setQueryData(qk.chatMessages(SESSION, chatJid), infinite<Message>([]));
    const chat: Chat = {
      id: 1,
      sessionId: SESSION,
      jid: chatJid,
      type: "dm",
      name: "x",
      unreadCount: 0,
      archived: false,
      pinned: false,
    };
    qc.setQueryData(qk.chat(SESSION, chatJid), chat);
    qc.setQueryData(qk.chats(SESSION), infinite([chat]));

    applyEvent(
      qc,
      evt("message", {
        waMessageId: "m1",
        chatJid,
        fromMe: false,
        type: "text",
        body: "hi",
        status: "delivered",
        timestamp: 2000,
      }),
    );

    const msgs = qc.getQueryData<InfiniteData<Page<Message>>>(
      qk.chatMessages(SESSION, chatJid),
    );
    expect(msgs?.pages[0]?.data[0]?.id).toBe("m1");
    expect(qc.getQueryData<Chat>(qk.chat(SESSION, chatJid))?.unreadCount).toBe(1);
    expect(qc.getQueryData<Chat>(qk.chat(SESSION, chatJid))?.lastMessageAt).toBe(2000);
  });

  it("refreshes a stored outbound send after showing it live", () => {
    const chatJid = "123@s.whatsapp.net";
    const canonicalJid = "123@lid";
    qc.setQueryData(qk.chatMessages(SESSION, chatJid), infinite<Message>([]));
    qc.setQueryData(qk.chatMessages(SESSION, canonicalJid), infinite<Message>([]));
    qc.setQueryData(qk.chat(SESSION, chatJid), { id: 1, jid: chatJid });
    qc.setQueryData(qk.chats(SESSION), infinite<Chat>([]));

    applyEvent(qc, evt("message.from_me", {
      waMessageId: "WA_1",
      chatJid,
      fromMe: true,
      type: "text",
      body: "bot reply",
      timestamp: 2000,
    }));

    const messages = qc.getQueryData<InfiniteData<Page<Message>>>(
      qk.chatMessages(SESSION, chatJid),
    );
    expect(messages?.pages[0]?.data[0]).toMatchObject({
      waMessageId: "WA_1",
      body: "bot reply",
      direction: "out",
    });
    expect(qc.getQueryState(qk.chatMessages(SESSION, chatJid))?.isInvalidated).toBe(true);
    expect(qc.getQueryState(qk.chatMessages(SESSION, canonicalJid))?.isInvalidated).toBe(true);
    expect(qc.getQueryState(qk.chat(SESSION, chatJid))?.isInvalidated).toBe(true);
    expect(qc.getQueryState(qk.chats(SESSION))?.isInvalidated).toBe(true);
  });

  it("message is idempotent (no duplicate on replay)", () => {
    const chatJid = "123@s.whatsapp.net";
    qc.setQueryData(qk.chatMessages(SESSION, chatJid), infinite<Message>([]));
    qc.setQueryData<Chat>(qk.chat(SESSION, chatJid), {
      id: 1,
      sessionId: SESSION,
      jid: chatJid,
      type: "dm",
      unreadCount: 0,
      archived: false,
      pinned: false,
    });
    const e = evt("message", {
      waMessageId: "m1",
      chatJid,
      fromMe: false,
      type: "text",
      body: "hi",
      timestamp: 2000,
    });
    applyEvent(qc, e);
    applyEvent(qc, e);
    const msgs = qc.getQueryData<InfiniteData<Page<Message>>>(
      qk.chatMessages(SESSION, chatJid),
    );
    expect(msgs?.pages[0]?.data).toHaveLength(1);
    expect(qc.getQueryData<Chat>(qk.chat(SESSION, chatJid))?.unreadCount).toBe(1);
  });

  it("message creates a cached chat row when the inbox is already loaded", () => {
    const chatJid = "new@s.whatsapp.net";
    qc.setQueryData(qk.chatMessages(SESSION, chatJid), infinite<Message>([]));
    qc.setQueryData(qk.chats(SESSION), infinite<Chat>([]));

    applyEvent(
      qc,
      evt("message.from_me", {
        waMessageId: "m1",
        chatJid,
        fromMe: true,
        type: "text",
        body: "hi",
        timestamp: 2000,
      }),
    );

    const chats = qc.getQueryData<InfiniteData<Page<Chat>>>(qk.chats(SESSION));
    expect(chats?.pages[0]?.data[0]).toMatchObject({
      jid: chatJid,
      lastMessageAt: 2000,
      unreadCount: 0,
    });
  });

  it("message bump merges aliased chat rows", () => {
    const lid = "abc@lid";
    const phone = "123@s.whatsapp.net";
    const oldChat: Chat = {
      id: 1,
      sessionId: SESSION,
      jid: lid,
      aliases: [lid, phone],
      type: "dm",
      name: "Alice",
      unreadCount: 0,
      archived: false,
      pinned: false,
      lastMessageAt: 1000,
    };
    const phoneChat: Chat = {
      ...oldChat,
      id: 2,
      jid: phone,
      unreadCount: 2,
      lastMessageAt: 1500,
    };
    qc.setQueryData(qk.chatMessages(SESSION, phone), infinite<Message>([]));
    qc.setQueryData(qk.chats(SESSION), infinite<Chat>([oldChat, phoneChat]));

    applyEvent(
      qc,
      evt("message", {
        id: "m2",
        waMessageId: "m2",
        chatJid: phone,
        direction: "in",
        type: "text",
        body: "hi",
        timestamp: 2000,
      }),
    );

    const chats = qc.getQueryData<InfiniteData<Page<Chat>>>(qk.chats(SESSION));
    expect(chats?.pages[0]?.data).toHaveLength(1);
    expect(chats?.pages[0]?.data[0]).toMatchObject({
      jid: phone,
      aliases: [lid, phone],
      unreadCount: 3,
      lastMessageAt: 2000,
    });
  });

  it("message replay dedupes by waMessageId", () => {
    const chatJid = "123@s.whatsapp.net";
    qc.setQueryData(
      qk.chatMessages(SESSION, chatJid),
      infinite<Message>([
        {
          id: "tmp_1",
          waMessageId: "w1",
          sessionId: SESSION,
          chatJid,
          direction: "out",
          fromMe: true,
          type: "text",
          body: "hi",
          timestamp: 1000,
          createdAt: 1000,
          deleted: false,
          edited: false,
          hasMedia: false,
        },
      ]),
    );

    applyEvent(
      qc,
      evt("message.from_me", {
        id: "real_1",
        waMessageId: "w1",
        chatJid,
        direction: "out",
        type: "text",
        body: "hi",
        timestamp: 1000,
      }),
    );

    const msgs = qc.getQueryData<InfiniteData<Page<Message>>>(
      qk.chatMessages(SESSION, chatJid),
    );
    expect(msgs?.pages[0]?.data).toHaveLength(1);
    expect(msgs?.pages[0]?.data[0]?.id).toBe("tmp_1");
  });

  it("does not regress message status on a reordered receipt", () => {
    const chatJid = "123@s.whatsapp.net";
    qc.setQueryData(
      qk.chatMessages(SESSION, chatJid),
      infinite<Message>([
        {
          id: "m1",
          sessionId: SESSION,
          chatJid,
          direction: "out",
          type: "text",
          body: "hi",
          status: "read",
          timestamp: 1000,
        } as Message,
      ]),
    );

    applyEvent(qc, evt("message.status", { messageIds: ["m1"], status: "delivered" }));

    const messages = qc.getQueryData<InfiniteData<Page<Message>>>(
      qk.chatMessages(SESSION, chatJid),
    );
    expect(messages?.pages[0]?.data[0]?.status).toBe("read");
  });

  it("message.status patches by messageId across chats (reconciles optimistic)", () => {
    const chatJid = "123@s.whatsapp.net";
    const m: Message = {
      id: "m1",
      sessionId: SESSION,
      chatJid,
      direction: "out",
      fromMe: true,
      type: "text",
      body: "hi",
      status: "pending",
      timestamp: 1,
      createdAt: 1,
      deleted: false,
      edited: false,
      hasMedia: false,
      waMessageId: "m1",
    };
    qc.setQueryData(qk.chatMessages(SESSION, chatJid), infinite([m]));

    applyEvent(qc, evt("message.status", { messageIds: ["m1"], status: "read" }));

    const msgs = qc.getQueryData<InfiniteData<Page<Message>>>(
      qk.chatMessages(SESSION, chatJid),
    );
    expect(msgs?.pages[0]?.data[0]?.status).toBe("read");
  });

  it("does not regress chat activity for an older message event", () => {
    const chatJid = "123@s.whatsapp.net";
    const chat: Chat = {
      id: 1,
      sessionId: SESSION,
      jid: chatJid,
      type: "dm",
      unreadCount: 2,
      lastMessageAt: 3000,
      archived: false,
      pinned: false,
    };
    qc.setQueryData(qk.chat(SESSION, chatJid), chat);
    qc.setQueryData(qk.chats(SESSION), infinite([chat]));
    qc.setQueryData(qk.chatMessages(SESSION, chatJid), infinite<Message>([]));

    applyEvent(
      qc,
      evt("message", {
        waMessageId: "older",
        chatJid,
        fromMe: false,
        type: "text",
        timestamp: 2000,
      }),
    );

    expect(qc.getQueryData<Chat>(qk.chat(SESSION, chatJid))).toMatchObject({
      lastMessageAt: 3000,
      unreadCount: 3,
    });
  });

  it("invalidates instead of fabricating a message from a partial payload", () => {
    const chatJid = "123@s.whatsapp.net";
    qc.setQueryData(qk.chatMessages(SESSION, chatJid), infinite<Message>([]));

    applyEvent(qc, evt("message", { chatJid, type: "text", timestamp: 2000 }));

    expect(qc.getQueryState(qk.chatMessages(SESSION, chatJid))?.isInvalidated).toBe(true);
    expect(
      qc.getQueryData<InfiniteData<Page<Message>>>(qk.chatMessages(SESSION, chatJid))
        ?.pages[0]?.data,
    ).toEqual([]);
  });

  it("invalidates the target timeline for edited wire payloads", () => {
    const chatJid = "123@s.whatsapp.net";
    qc.setQueryData(qk.chatMessages(SESSION, chatJid), infinite<Message>([]));

    applyEvent(
      qc,
      evt("message.edited", {
        waMessageId: "edit-event",
        targetId: "original-message",
        chatJid,
        type: "edit",
        body: "changed",
        timestamp: 2000,
      }),
    );

    expect(qc.getQueryState(qk.chatMessages(SESSION, chatJid))?.isInvalidated).toBe(true);
  });

  it("presence.update caches typing state by chatJid", () => {
    const chatJid = "123@s.whatsapp.net";
    applyEvent(
      qc,
      evt("presence.update", {
        chatJid,
        from: "123@s.whatsapp.net",
        state: "composing",
      }),
    );

    expect(qc.getQueryData(qk.presence(SESSION, chatJid))).toMatchObject({
      state: "composing",
    });
  });

  it("ignores unknown events without throwing", () => {
    expect(() => applyEvent(qc, evt("call.incoming", { from: "x" }))).not.toThrow();
    expect(() => applyEvent(qc, evt("totally.unknown", {}))).not.toThrow();
  });
});

describe("message lifecycle cache", () => {
  const chatJid = "123@g.us";
  const original: Message = {
    id: "row-1", waMessageId: "original", sessionId: SESSION, chatJid,
    type: "text", body: "old text", direction: "in", fromMe: false,
    timestamp: 1, createdAt: 1, edited: false, deleted: false, hasMedia: false,
  };

  it("patches the original row on an older page and keeps other chats unchanged", () => {
    const qc = new QueryClient();
    const key = qk.chatMessages(SESSION, chatJid);
    qc.setQueryData(key, {
      pageParams: [undefined, "older"],
      pages: [{ data: [], nextCursor: "older" }, { data: [original], nextCursor: null }],
    });
    const otherKey = qk.chatMessages(SESSION, "other@g.us");
    qc.setQueryData(otherKey, infinite([original]));
    applyEvent(qc, evt("message.edited", {
      chatJid, waMessageId: "edit-command", targetId: "original", body: "new text",
    }));
    const data = qc.getQueryData<InfiniteData<Page<Message>>>(key);
    expect(data?.pages[1]?.data).toEqual([{ ...original, body: "new text", edited: true }]);
    expect(qc.getQueryData<InfiniteData<Page<Message>>>(otherKey)?.pages[0]?.data).toEqual([original]);
  });

  it("marks a revoked message deleted and does not restore it with a later edit", () => {
    const qc = new QueryClient();
    const key = qk.chatMessages(SESSION, chatJid);
    qc.setQueryData(key, infinite([original]));
    applyEvent(qc, evt("message.revoked", { chatJid, targetId: "original" }));
    applyEvent(qc, evt("message.edited", { chatJid, targetId: "original", body: "late text" }));
    expect(qc.getQueryData<InfiniteData<Page<Message>>>(key)?.pages[0]?.data).toEqual([
      { ...original, deleted: true },
    ]);
  });
});

describe("attachment lifecycle events", () => {
  it.each(["media.ready", "media.expired"])("%s refreshes message URLs for the session", (name) => {
    const qc = new QueryClient();
    const key = qk.chatMessages(SESSION, "chat");
    const other = qk.chatMessages("other_session", "chat");
    qc.setQueryData(key, infinite([]));
    qc.setQueryData(other, infinite([]));
    applyEvent(qc, evt(name, { id: "asset", waMessageId: "message" }));
    expect(qc.getQueryState(key)?.isInvalidated).toBe(true);
    expect(qc.getQueryState(other)?.isInvalidated).toBe(false);
  });
});
