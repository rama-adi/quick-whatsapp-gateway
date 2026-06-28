// Viewer surface — server-side hybrid READS (§6.2). Colocated server functions
// that read the GATEWAY-OWNED `chats` / `messages` tables directly via Drizzle
// for SSR/loader hydration, then map rows into the SAME OpenAPI DTO shapes the
// gateway REST API returns (Chat / Message / Page<T>) so the client hooks
// (useChats / useChat / useChatMessages) hydrate from the seeded cache and the
// NDJSON cacheBridge targets the identical qk.* keys.
//
// READ-ONLY: these never write WA tables (single-writer = gateway, §6.2). The
// frontend is gated to the active org: a session must belong to the caller's
// active organization (wa_sessions.organization_id) before we expose its data.
//
// Pagination mirrors the gateway's cursor lists: limit + opaque cursor. We use a
// opaque sortable row cursor (rows.id); nextCursor is the last row's id,
// matching Page<T> = {data, nextCursor}.

import { createServerFn } from "@tanstack/react-start";
import { authMiddleware } from "~/lib/auth/middleware";
import type { Chat, Message } from "~/lib/api/types";
import type { Page } from "~/lib/api/envelope";

const PAGE_LIMIT = 50;

// The generated Message schema types `mentions` as `unknown` (the gateway emits a
// free-form JSON array). TanStack's server-fn serializer rejects `unknown`, so we
// narrow it to the concrete wire shape (a JID string array) here. `string[]` is
// assignable to `unknown`, so a SerializableMessage is still a valid Message for
// the cache consumers.
type SerializableMessage = Omit<Message, "mentions"> & { mentions?: string[] };

/** Throws (redirect handled upstream) unless the session is in the active org. */
async function assertSessionInActiveOrg(
  sessionId: string,
  activeOrgId: string | null,
): Promise<boolean> {
  if (!activeOrgId) return false;
  const { db } = await import("~/lib/db");
  const { waSessions } = await import("~/lib/db/wa");
  const { and, eq } = await import("drizzle-orm");
  const rows = await db
    .select({ id: waSessions.id })
    .from(waSessions)
    .where(
      and(
        eq(waSessions.id, sessionId),
        eq(waSessions.organizationId, activeOrgId),
      ),
    )
    .limit(1);
  return rows.length > 0;
}

/**
 * SSR seed for the chats list (page 0). Cursor-paginated by recency; ordered by
 * lastMessageAt desc to match the viewer's "most recent first" list.
 */
export const fetchChatsPage = createServerFn({ method: "GET" })
  .middleware([authMiddleware])
  .validator((input: { sessionId: string; cursor?: string }) => input)
  .handler(async ({ data, context }): Promise<Page<Chat>> => {
    const { sessionId, cursor } = data;
    const ok = await assertSessionInActiveOrg(
      sessionId,
      context.activeOrg?.id ?? null,
    );
    if (!ok) return { data: [], nextCursor: null };

    const { db } = await import("~/lib/db");
    const { chats, whatsappGroups } = await import("~/lib/db/wa");
    const { and, eq, lt, desc, sql } = await import("drizzle-orm");

    const cursorId = cursor !== undefined ? Number(cursor) : undefined;
    const where =
      cursorId !== undefined && Number.isFinite(cursorId)
        ? and(eq(chats.sessionId, sessionId), lt(chats.id, cursorId))
        : eq(chats.sessionId, sessionId);

    const rows = await db
      .select({
        id: chats.id,
        sessionId: chats.sessionId,
        chatJid: chats.chatJid,
        type: chats.type,
        // Groups display their subject (whatsapp_groups); non-groups fall back to
        // chats.name. Mirrors the gateway REST resolution so SSR + live agree.
        name: sql<string | null>`COALESCE(${whatsappGroups.subject}, ${chats.name})`,
        unreadCount: chats.unreadCount,
        archived: chats.archived,
        pinned: chats.pinned,
        mutedUntil: chats.mutedUntil,
        lastMessageAt: chats.lastMessageAt,
      })
      .from(chats)
      .leftJoin(whatsappGroups, eq(whatsappGroups.groupJid, chats.chatJid))
      .where(where)
      .orderBy(desc(chats.id))
      .limit(PAGE_LIMIT + 1);

    const hasMore = rows.length > PAGE_LIMIT;
    const pageRows = hasMore ? rows.slice(0, PAGE_LIMIT) : rows;

    return {
      data: pageRows.map(rowToChat),
      nextCursor: hasMore ? String(pageRows[pageRows.length - 1]?.id) : null,
    };
  });

/** SSR seed for a single chat (timeline header). */
export const fetchChat = createServerFn({ method: "GET" })
  .middleware([authMiddleware])
  .validator((input: { sessionId: string; chatJid: string }) => input)
  .handler(async ({ data, context }): Promise<Chat | null> => {
    const { sessionId, chatJid } = data;
    const ok = await assertSessionInActiveOrg(
      sessionId,
      context.activeOrg?.id ?? null,
    );
    if (!ok) return null;

    const { db } = await import("~/lib/db");
    const { chats, whatsappGroups } = await import("~/lib/db/wa");
    const { and, eq, sql } = await import("drizzle-orm");

    const rows = await db
      .select({
        id: chats.id,
        sessionId: chats.sessionId,
        chatJid: chats.chatJid,
        type: chats.type,
        name: sql<string | null>`COALESCE(${whatsappGroups.subject}, ${chats.name})`,
        unreadCount: chats.unreadCount,
        archived: chats.archived,
        pinned: chats.pinned,
        mutedUntil: chats.mutedUntil,
        lastMessageAt: chats.lastMessageAt,
      })
      .from(chats)
      .leftJoin(whatsappGroups, eq(whatsappGroups.groupJid, chats.chatJid))
      .where(and(eq(chats.sessionId, sessionId), eq(chats.chatJid, chatJid)))
      .limit(1);
    const row = rows[0];
    return row ? rowToChat(row) : null;
  });

/**
 * SSR seed for a chat's message timeline (page 0 = newest). Ordered newest-first
 * by (timestamp, id) so the cursor is stable; the client flips to chronological
 * for display. Live inbound messages are prepended by the cacheBridge.
 */
export const fetchMessagesPage = createServerFn({ method: "GET" })
  .middleware([authMiddleware])
  .validator(
    (input: { sessionId: string; chatJid: string; cursor?: string }) => input,
  )
  .handler(async ({ data, context }): Promise<Page<SerializableMessage>> => {
    const { sessionId, chatJid, cursor } = data;
    const ok = await assertSessionInActiveOrg(
      sessionId,
      context.activeOrg?.id ?? null,
    );
    if (!ok) return { data: [], nextCursor: null };

    const { db } = await import("~/lib/db");
    const { messages, whatsappIdentities } = await import("~/lib/db/wa");
    const { and, eq, lt, desc, or, inArray } = await import("drizzle-orm");

    // messages.id is a sortable string ULID, so the cursor is the id itself
    // (lexicographic compare) — no numeric parse.
    const base = and(
      eq(messages.sessionId, sessionId),
      eq(messages.chatJid, chatJid),
    );
    const where = cursor ? and(base, lt(messages.id, cursor)) : base;

    const rows = await db
      .select({
        id: messages.id,
        sessionId: messages.sessionId,
        waMessageId: messages.waMessageId,
        chatJid: messages.chatJid,
        senderJid: messages.senderJid,
        senderLid: messages.senderLid,
        // Sender's resolved display name (whatsapp_identities, keyed by LID) so
        // group messages can label each author; null when unknown.
        senderName: whatsappIdentities.name,
        direction: messages.direction,
        type: messages.type,
        body: messages.body,
        mentions: messages.mentions,
        status: messages.status,
        fromMe: messages.fromMe,
        hasMedia: messages.hasMedia,
        edited: messages.edited,
        deleted: messages.deleted,
        timestamp: messages.timestamp,
        createdAt: messages.createdAt,
      })
      .from(messages)
      .leftJoin(
        whatsappIdentities,
        eq(whatsappIdentities.lid, messages.senderLid),
      )
      .where(where)
      .orderBy(desc(messages.id))
      .limit(PAGE_LIMIT + 1);

    const hasMore = rows.length > PAGE_LIMIT;
    const pageRows = hasMore ? rows.slice(0, PAGE_LIMIT) : rows;

    // Resolve @-mentions to display names (mirrors the gateway's ChatService):
    // gather the mention JIDs across the page, look them up once by lid/phone_jid,
    // and key the result by user-part so it matches the "@<number>" token in body.
    const mentionJids = new Set<string>();
    for (const r of pageRows) {
      for (const j of parseMentions(r.mentions)) mentionJids.add(j);
    }
    const nameByUserPart: Record<string, string> = {};
    if (mentionJids.size > 0) {
      const jidList = [...mentionJids];
      const idRows = await db
        .select({
          lid: whatsappIdentities.lid,
          phoneJid: whatsappIdentities.phoneJid,
          name: whatsappIdentities.name,
        })
        .from(whatsappIdentities)
        .where(
          or(
            inArray(whatsappIdentities.lid, jidList),
            inArray(whatsappIdentities.phoneJid, jidList),
          ),
        );
      const byId: Record<string, string> = {};
      for (const ir of idRows) {
        if (!ir.name) continue;
        byId[ir.lid] = ir.name;
        if (ir.phoneJid) byId[ir.phoneJid] = ir.name;
      }
      for (const j of jidList) {
        const n = byId[j];
        if (n) nameByUserPart[jidUserPart(j)] = n;
      }
    }

    return {
      data: pageRows.map((r) => rowToMessage(r, nameByUserPart)),
      nextCursor: hasMore ? String(pageRows[pageRows.length - 1]?.id) : null,
    };
  });

/** Token before "@" (and any ":device" suffix) — the form WhatsApp embeds as
 * "@<userpart>" in a message body. */
function jidUserPart(jid: string): string {
  const i = jid.search(/[@:]/);
  return i >= 0 ? jid.slice(0, i) : jid;
}

/** Decode a messages.mentions value (JSON array of JID strings). Drizzle returns
 * a parsed array for the JSON column, but tolerate a raw string too. */
function parseMentions(raw: unknown): string[] {
  if (Array.isArray(raw)) {
    return raw.filter((x): x is string => typeof x === "string");
  }
  if (typeof raw === "string" && raw) {
    try {
      const v: unknown = JSON.parse(raw);
      return Array.isArray(v)
        ? v.filter((x): x is string => typeof x === "string")
        : [];
    } catch {
      return [];
    }
  }
  return [];
}

// ===== row -> DTO mappers (mirror the gateway's REST response shapes) =====

type ChatRow = {
  id: number;
  sessionId: string;
  chatJid: string;
  type: Chat["type"];
  name: string | null;
  unreadCount: number;
  archived: number;
  pinned: number;
  mutedUntil: number | null;
  lastMessageAt: number | null;
};

function rowToChat(r: ChatRow): Chat {
  return {
    id: r.id,
    sessionId: r.sessionId,
    jid: r.chatJid,
    type: r.type,
    name: r.name ?? undefined,
    unreadCount: r.unreadCount,
    archived: Boolean(r.archived),
    pinned: Boolean(r.pinned),
    mutedUntil: r.mutedUntil ?? undefined,
    lastMessageAt: r.lastMessageAt ?? undefined,
  };
}

type MessageRow = {
  sessionId: string;
  waMessageId: string;
  chatJid: string;
  senderJid: string | null;
  senderLid: string | null;
  senderName: string | null;
  direction: NonNullable<Message["direction"]>;
  type: string;
  body: string | null;
  mentions: unknown;
  status: Message["status"] | null;
  fromMe: number;
  hasMedia: number;
  edited: number;
  deleted: number;
  timestamp: number;
  createdAt: number;
};

function rowToMessage(
  r: MessageRow,
  nameByUserPart: Record<string, string>,
): SerializableMessage {
  const mentions = parseMentions(r.mentions);
  let mentionNames: Record<string, string> | undefined;
  if (mentions.length > 0) {
    const m: Record<string, string> = {};
    for (const jid of mentions) {
      const up = jidUserPart(jid);
      if (nameByUserPart[up]) m[up] = nameByUserPart[up];
    }
    if (Object.keys(m).length > 0) mentionNames = m;
  }
  return {
    id: r.waMessageId,
    sessionId: r.sessionId,
    waMessageId: r.waMessageId,
    chatJid: r.chatJid,
    senderJid: r.senderJid ?? undefined,
    senderLid: r.senderLid ?? undefined,
    senderName: r.senderName ?? undefined,
    direction: r.direction,
    type: r.type,
    body: r.body ?? undefined,
    mentions: mentions.length > 0 ? mentions : undefined,
    mentionNames,
    fromMe: Boolean(r.fromMe),
    hasMedia: Boolean(r.hasMedia),
    edited: Boolean(r.edited),
    deleted: Boolean(r.deleted),
    timestamp: r.timestamp,
    createdAt: r.createdAt,
    status: r.status ?? undefined,
  };
}
