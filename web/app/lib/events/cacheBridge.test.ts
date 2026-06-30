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

  it("auth.qr seeds the live QR query", () => {
    applyEvent(qc, evt("auth.qr", { code: "2@abc" }));
    expect(qc.getQueryData(qk.sessionQR(SESSION))).toEqual({ code: "2@abc" });
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
        id: "m1",
        chatJid,
        direction: "in",
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

  it("message is idempotent (no duplicate on replay)", () => {
    const chatJid = "123@s.whatsapp.net";
    qc.setQueryData(qk.chatMessages(SESSION, chatJid), infinite<Message>([]));
    const e = evt("message", {
      id: "m1",
      chatJid,
      direction: "in",
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
  });

  it("message creates a cached chat row when the inbox is already loaded", () => {
    const chatJid = "new@s.whatsapp.net";
    qc.setQueryData(qk.chatMessages(SESSION, chatJid), infinite<Message>([]));
    qc.setQueryData(qk.chats(SESSION), infinite<Chat>([]));

    applyEvent(
      qc,
      evt("message.from_me", {
        id: "m1",
        chatJid,
        direction: "out",
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

    applyEvent(qc, evt("message.status", { messageId: "m1", status: "read" }));

    const msgs = qc.getQueryData<InfiniteData<Page<Message>>>(
      qk.chatMessages(SESSION, chatJid),
    );
    expect(msgs?.pages[0]?.data[0]?.status).toBe("read");
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
