// Event → query-cache reducer. Translates §9 event envelopes into immutable
// updates over the TanStack Query cache keyed by qk.* and e.session.
// FROZEN — owned by the foundation agent. Pure-ish + unit-tested (cacheBridge.test.ts).
//
// Also forwards every event to the eventBus (firehose). Unknown event types
// fall through to the bus only (forward-compatible).

import type { InfiniteData, QueryClient } from "@tanstack/react-query";
import { qk } from "../query";
import type {
  Chat,
  EventEnvelope,
  Message,
  WASession,
} from "../api/types";
import type { Page } from "../api/envelope";
import { publishEvent } from "./eventBus";

type Infinite<T> = InfiniteData<Page<T>, string | undefined>;

/** Narrow an unknown payload field to a string. */
function str(v: unknown): string | undefined {
  return typeof v === "string" ? v : undefined;
}

/** Map over the pages of an InfiniteData, preserving pageParams. */
function mapPages<T>(
  data: Infinite<T> | undefined,
  fn: (items: T[]) => T[],
): Infinite<T> | undefined {
  if (!data) return data;
  return {
    ...data,
    pages: data.pages.map((p) => ({ ...p, data: fn(p.data) })),
  };
}

/** Patch the first row matching `match` across all pages; no-op if absent. */
function patchRow<T>(
  qc: QueryClient,
  key: readonly unknown[],
  match: (item: T) => boolean,
  patch: (item: T) => T,
): void {
  qc.setQueryData<Infinite<T>>(key, (data) =>
    mapPages(data, (items) => items.map((it) => (match(it) ? patch(it) : it))),
  );
}

/** Apply one event to the cache. Pure w.r.t. its inputs aside from qc mutation. */
export function applyEvent(qc: QueryClient, e: EventEnvelope): void {
  const s = e.session;
  const p = (e.payload ?? {}) as Record<string, unknown>;

  switch (e.event) {
    case "session.status": {
      const status = str(p.status) as WASession["status"] | undefined;
      if (status && s) {
        qc.setQueryData<WASession>(qk.session(s), (cur) =>
          cur ? { ...cur, status } : cur,
        );
        const patch = (row: WASession): WASession =>
          row.id === s ? { ...row, status } : row;
        patchRow<WASession>(qc, qk.sessions(), (r) => r.id === s, patch);
        patchRow<WASession>(qc, qk.adminSessions(), (r) => r.id === s, patch);
      }
      break;
    }

    case "auth.qr": {
      const code = str(p.code);
      if (s && code) qc.setQueryData(qk.sessionQR(s), { code });
      break;
    }

    case "auth.code": {
      if (s) qc.setQueryData(qk.sessionPairing(s), p);
      break;
    }

    case "message":
    case "message.from_me": {
      const chatJid = str(p.chatJid);
      if (!s || !chatJid) break;
      const msg = p as unknown as Message;
      // Prepend to page 0 of the chat's message list (dedup by id).
      qc.setQueryData<Infinite<Message>>(qk.chatMessages(s, chatJid), (data) => {
        if (!data || data.pages.length === 0) return data;
        const exists = data.pages.some((pg) => pg.data.some((m) => m.id === msg.id));
        if (exists) return data;
        const [first, ...rest] = data.pages;
        if (!first) return data;
        return {
          ...data,
          pages: [{ ...first, data: [msg, ...first.data] }, ...rest],
        };
      });
      // Bump the chat's lastMessageAt + unread, then resort chats page 0.
      bumpChat(qc, s, chatJid, msg, e.event === "message");
      break;
    }

    case "message.status": {
      const messageId = str(p.messageId) ?? str(p.id);
      const status = str(p.status) as Message["status"] | undefined;
      if (!s || !messageId || !status) break;
      // The message id is globally unique, so patch across every cached chat.
      const chatKeyRoot = qk.chats(s); // ["sessions", s, "chats"]
      patchMessageEverywhere(qc, chatKeyRoot, messageId, (m) => ({ ...m, status }));
      break;
    }

    case "message.reaction":
    case "message.edited":
    case "message.revoked":
    case "poll.vote": {
      const s2 = s;
      const messageId = str(p.messageId) ?? str(p.id);
      const body = str(p.body);
      if (!s2 || !messageId) break;
      patchMessageEverywhere(qc, qk.chats(s2), messageId, (m) =>
        body !== undefined ? { ...m, body } : m,
      );
      break;
    }

    case "chat.update": {
      const chatJid = str(p.jid) ?? str(p.chatJid);
      if (!s || !chatJid) break;
      qc.setQueryData<Chat>(qk.chat(s, chatJid), (cur) =>
        cur ? { ...cur, ...(p as Partial<Chat>) } : cur,
      );
      patchRow<Chat>(
        qc,
        qk.chats(s),
        (c) => c.jid === chatJid,
        (c) => ({ ...c, ...(p as Partial<Chat>) }),
      );
      break;
    }

    case "contact.update": {
      const lid = str(p.lid);
      if (s && lid) {
        qc.invalidateQueries({ queryKey: qk.contact(s, lid) });
      }
      if (s) {
        // Filtered contact lists are keyed by an object filter; invalidate the root.
        qc.invalidateQueries({ queryKey: ["sessions", s, "contacts"] });
      }
      break;
    }

    case "group.update":
    case "group.participant": {
      if (s) qc.invalidateQueries({ queryKey: qk.groups(s) });
      break;
    }

    case "presence.update": {
      const jid = str(p.chatJid) ?? str(p.jid) ?? str(p.from);
      if (s && jid) qc.setQueryData(qk.presence(s, jid), p);
      break;
    }

    // call.incoming, newsletter.update, ping-less unknowns: bus/monitor only.
    default:
      break;
  }

  // Every data frame also feeds the firehose for the monitor + surface tails.
  publishEvent(e);
}

function bumpChat(
  qc: QueryClient,
  s: string,
  chatJid: string,
  msg: Message,
  incoming: boolean,
): void {
  const ts = typeof msg.timestamp === "number" ? msg.timestamp : Date.now();
  qc.setQueryData<Chat>(qk.chat(s, chatJid), (cur) =>
    cur
      ? {
          ...cur,
          lastMessageAt: ts,
          unreadCount: incoming ? (cur.unreadCount ?? 0) + 1 : cur.unreadCount,
        }
      : cur,
  );
  qc.setQueryData<Infinite<Chat>>(qk.chats(s), (data) => {
    if (!data || data.pages.length === 0) return data;
    let found = false;
    const updated = mapPages(data, (chats) =>
      chats.map((c) => {
        if (c.jid !== chatJid) return c;
        found = true;
        return {
          ...c,
          lastMessageAt: ts,
          unreadCount: incoming ? (c.unreadCount ?? 0) + 1 : c.unreadCount,
        };
      }),
    );
    if (!updated) return data;
    // Resort page 0 by lastMessageAt desc so the active chat floats to top.
    const [first, ...rest] = updated.pages;
    if (!first) return updated;
    const firstRows = found
      ? first.data
      : [
          {
            id: 0,
            sessionId: s,
            jid: chatJid,
            type: "dm",
            lastMessageAt: ts,
            unreadCount: incoming ? 1 : 0,
            archived: false,
            pinned: false,
          } satisfies Chat,
          ...first.data,
        ];
    const sorted = [...firstRows].sort(
      (a, b) => (b.lastMessageAt ?? 0) - (a.lastMessageAt ?? 0),
    );
    return { ...updated, pages: [{ ...first, data: sorted }, ...rest] };
  });
}

/**
 * Patch a message by id across every chat-messages query under a session.
 * Uses the query cache index to find ["sessions",s,"chats",cid,"messages"].
 */
function patchMessageEverywhere(
  qc: QueryClient,
  chatsRoot: readonly unknown[],
  messageId: string,
  patch: (m: Message) => Message,
): void {
  const cache = qc.getQueryCache();
  for (const query of cache.getAll()) {
    const key = query.queryKey;
    if (!isMessagesKey(key, chatsRoot)) continue;
    qc.setQueryData<Infinite<Message>>(key as readonly unknown[], (data) =>
      mapPages(data, (msgs) =>
        msgs.map((m) => (m.id === messageId ? patch(m) : m)),
      ),
    );
  }
}

/** True for keys shaped ["sessions",s,"chats",<cid>,"messages"]. */
function isMessagesKey(
  key: readonly unknown[],
  chatsRoot: readonly unknown[],
): boolean {
  if (key.length !== chatsRoot.length + 2) return false;
  for (let i = 0; i < chatsRoot.length; i++) {
    if (key[i] !== chatsRoot[i]) return false;
  }
  return key[key.length - 1] === "messages";
}
