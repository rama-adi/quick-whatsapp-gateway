// Event → query-cache reducer. Translates §9 event envelopes into immutable
// updates over the TanStack Query cache keyed by qk.* and e.session.
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
        const applyStatus = (row: WASession): WASession =>
          status === "logged_out"
            ? {
                ...row,
                status,
                waJid: undefined,
                waLid: undefined,
                phoneNumber: undefined,
              }
            : { ...row, status };
        qc.setQueryData<WASession>(qk.session(s), (cur) =>
          cur ? applyStatus(cur) : cur,
        );
        const patch = (row: WASession): WASession =>
          row.id === s ? applyStatus(row) : row;
        patchRow<WASession>(qc, qk.sessions(), (r) => r.id === s, patch);
        patchRow<WASession>(qc, qk.adminSessions(), (r) => r.id === s, patch);
        if (status === "logged_out") {
          qc.removeQueries({ queryKey: qk.sessionQR(s), exact: true });
          qc.removeQueries({ queryKey: qk.sessionPairing(s), exact: true });
        }
        if (status === "working") {
          // PairSuccess persists the JID before Connected emits WORKING. Refresh
          // rows now so the UI switches from pairing controls to the attached
          // identity without requiring degraded-stream polling.
          void qc.invalidateQueries({ queryKey: qk.session(s), exact: true });
          void qc.invalidateQueries({ queryKey: qk.sessions() });
          void qc.invalidateQueries({ queryKey: qk.adminSessions() });
        }
      }
      break;
    }

    case "auth.qr": {
      const code = str(p.code);
      if (s && code) qc.setQueryData(qk.sessionQR(s), { code });
      break;
    }

    case "auth.code": {
      if (s) {
        qc.removeQueries({ queryKey: qk.sessionQR(s), exact: true });
        qc.removeQueries({ queryKey: qk.sessionPairing(s), exact: true });
        void qc.invalidateQueries({ queryKey: qk.session(s), exact: true });
        void qc.invalidateQueries({ queryKey: qk.sessions(), exact: true });
      }
      break;
    }

    case "message":
    case "message.from_me":
    case "message.interactive_reply": {
      const chatJid = str(p.chatJid);
      if (!s || !chatJid) break;
      const msg = projectMessage(s, e.event, p);
      if (!msg) {
        invalidateChatData(qc, s, chatJid);
        break;
      }
      let inserted = false;
      let timelineLoaded = false;
      qc.setQueryData<Infinite<Message>>(qk.chatMessages(s, chatJid), (data) => {
        if (!data || data.pages.length === 0) return data;
        timelineLoaded = true;
        const msgKey = messageKey(msg);
        const exists = data.pages.some((pg) =>
          pg.data.some((m) => messageKey(m) === msgKey),
        );
        if (exists) return data;
        inserted = true;
        const [first, ...rest] = data.pages;
        if (!first) return data;
        return normalizeMessagePages({
          ...data,
          pages: [{ ...first, data: [msg, ...first.data] }, ...rest],
        });
      });
      if (inserted) {
        bumpChat(qc, s, chatJid, msg, e.event === "message");
      } else if (!timelineLoaded) {
        // Without the timeline we cannot distinguish a new event from replay.
        // Refetch instead of risking a duplicate unread increment.
        invalidateChatData(qc, s, chatJid);
      }
      break;
    }

    case "media.ready":
    case "media.expired": {
      if (s) void qc.invalidateQueries({ queryKey: qk.chats(s) });
      break;
    }
    case "message.status": {
      const messageIds = Array.isArray(p.messageIds)
        ? p.messageIds.filter((id): id is string => typeof id === "string" && id.length > 0)
        : [];
      const status = str(p.status) as Message["status"] | undefined;
      if (!s || messageIds.length === 0 || !status) break;
      const chatKeyRoot = qk.chats(s); // ["sessions", s, "chats"]
      for (const messageId of messageIds) {
        patchMessageEverywhere(qc, chatKeyRoot, messageId, (m) =>
          advancesMessageStatus(m.status, status) ? { ...m, status } : m,
        );
      }
      break;
    }

    case "message.edited":
    case "message.revoked": {
      const chatJid = str(p.chatJid);
      const targetId = str(p.targetId);
      if (s && chatJid && targetId) {
        qc.setQueryData<Infinite<Message>>(qk.chatMessages(s, chatJid), (data) =>
          mapPages(data, (messages) => messages.map((message) => {
            if (message.waMessageId !== targetId && message.id !== targetId) return message;
            if (e.event === "message.revoked") return { ...message, deleted: true };
            if (typeof p.body !== "string" || message.deleted) return message;
            return { ...message, body: p.body, edited: true };
          })),
        );
      }
      if (s && chatJid) invalidateChatData(qc, s, chatJid);
      else if (s) void qc.invalidateQueries({ queryKey: qk.chats(s) });
      break;
    }

    case "message.reaction":
    case "poll.vote": {
      const chatJid = str(p.chatJid);
      if (s && chatJid) invalidateChatData(qc, s, chatJid);
      else if (s) void qc.invalidateQueries({ queryKey: qk.chats(s) });
      break;
    }

    case "chat.update": {
      const chatJid = str(p.chatJid);
      if (!s || !chatJid) break;
      // The wire payload describes a picture change, not a REST Chat patch.
      invalidateChatData(qc, s, chatJid);
      break;
    }

    case "contact.update": {
      if (s) {
        // The event identifies a WhatsApp JID while detail routes may be keyed
        // by a LID alias, so invalidate the full contact subtree.
        void qc.invalidateQueries({ queryKey: ["sessions", s, "contacts"] });
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

function projectMessage(
  sessionId: string,
  event: "message" | "message.from_me" | "message.interactive_reply",
  payload: Record<string, unknown>,
): Message | null {
  const waMessageId = str(payload.waMessageId);
  const chatJid = str(payload.chatJid);
  const type = str(payload.type);
  const timestamp = payload.timestamp;
  if (
    !waMessageId ||
    !chatJid ||
    !type ||
    typeof timestamp !== "number" ||
    !Number.isFinite(timestamp)
  ) {
    return null;
  }
  const fromMe =
    typeof payload.fromMe === "boolean"
      ? payload.fromMe
      : event === "message.from_me";
  return {
    id: waMessageId,
    waMessageId,
    sessionId,
    chatJid,
    direction: fromMe ? "out" : "in",
    fromMe,
    type,
    body: str(payload.body),
    senderJid: str(payload.senderJid),
    senderLid: str(payload.senderLid),
    quotedMessageId: str(payload.quotedMessageId),
    hasMedia: payload.hasMedia === true,
    timestamp,
    createdAt: timestamp,
    deleted: false,
    edited: false,
  };
}

function invalidateChatData(qc: QueryClient, sessionId: string, chatJid: string): void {
  void qc.invalidateQueries({ queryKey: qk.chatMessages(sessionId, chatJid), exact: true });
  void qc.invalidateQueries({ queryKey: qk.chat(sessionId, chatJid), exact: true });
  void qc.invalidateQueries({ queryKey: qk.chats(sessionId), exact: true });
}

const MESSAGE_STATUS_ORDER: Readonly<Record<string, number>> = {
  pending: 0,
  sent: 1,
  delivered: 2,
  read: 3,
  played: 4,
  failed: 5,
};

/** WhatsApp receipts can arrive out of order; projected status only advances. */
function advancesMessageStatus(
  current: Message["status"],
  next: Message["status"],
): boolean {
  const currentRank = current ? MESSAGE_STATUS_ORDER[current] : undefined;
  const nextRank = next ? MESSAGE_STATUS_ORDER[next] : undefined;
  if (nextRank === undefined) return false;
  return currentRank === undefined || nextRank >= currentRank;
}

function bumpChat(
  qc: QueryClient,
  s: string,
  chatJid: string,
  msg: Message,
  incoming: boolean,
): void {
  const ts = msg.timestamp;
  qc.setQueryData<Chat>(qk.chat(s, chatJid), (cur) =>
    cur
      ? {
          ...cur,
          lastMessageAt: Math.max(cur.lastMessageAt ?? 0, ts),
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
          lastMessageAt: Math.max(c.lastMessageAt ?? 0, ts),
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
    const sorted = normalizeChatRows(firstRows);
    return normalizeChatPages({
      ...updated,
      pages: [{ ...first, data: sorted }, ...rest],
    });
  });
}

function messageKey(m: Message): string {
  return m.waMessageId || m.id || `${m.chatJid}:${m.timestamp}:${m.direction}`;
}

function normalizeMessagePages(
  data: Infinite<Message> | undefined,
): Infinite<Message> | undefined {
  if (!data) return data;
  const seen = new Set<string>();
  return {
    ...data,
    pages: data.pages.map((page) => {
      const rows: Message[] = [];
      for (const msg of page.data) {
        const key = messageKey(msg);
        if (seen.has(key)) continue;
        seen.add(key);
        rows.push(msg);
      }
      return { ...page, data: rows };
    }),
  };
}

function chatAliases(c: Chat): string[] {
  const aliases = Array.isArray(c.aliases) ? c.aliases : [];
  return [...new Set([c.jid, ...aliases].filter(Boolean) as string[])];
}

function chatKey(c: Chat): string {
  const aliases = chatAliases(c);
  return aliases.length > 0 ? aliases.sort().join("|") : c.jid ?? "";
}

function chatMatches(c: Chat, jid: string): boolean {
  return chatAliases(c).includes(jid);
}

function normalizeChatRows(rows: Chat[]): Chat[] {
  const byKey = new Map<string, Chat>();
  for (const row of rows) {
    const key = chatKey(row);
    const prev = byKey.get(key);
    if (!prev) {
      byKey.set(key, row);
      continue;
    }
    const newer = (row.lastMessageAt ?? 0) >= (prev.lastMessageAt ?? 0) ? row : prev;
    byKey.set(key, {
      ...prev,
      ...newer,
      aliases: [...new Set([...chatAliases(prev), ...chatAliases(row)])],
      unreadCount: Math.max(prev.unreadCount ?? 0, row.unreadCount ?? 0),
      pinned: Boolean(prev.pinned || row.pinned),
      archived: Boolean(prev.archived && row.archived),
      mutedUntil: row.mutedUntil ?? prev.mutedUntil,
      name: row.name ?? prev.name,
    });
  }
  return [...byKey.values()].sort(
    (a, b) => (b.lastMessageAt ?? 0) - (a.lastMessageAt ?? 0),
  );
}

function normalizeChatPages(
  data: Infinite<Chat> | undefined,
): Infinite<Chat> | undefined {
  if (!data) return data;
  const seen = new Set<string>();
  return {
    ...data,
    pages: data.pages.map((page) => {
      const rows = [];
      for (const chat of normalizeChatRows(page.data)) {
        const key = chatKey(chat);
        if (seen.has(key)) continue;
        seen.add(key);
        rows.push(chat);
      }
      return { ...page, data: rows };
    }),
  };
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
        msgs.map((m) =>
          m.id === messageId || m.waMessageId === messageId ? patch(m) : m,
        ),
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
