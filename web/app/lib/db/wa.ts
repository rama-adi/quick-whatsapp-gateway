// READ-ONLY mirror of the gateway-owned schema; regenerate with
// `drizzle-kit introspect` against the migrated DB — DO NOT write these tables.
//
// These tables are migrated and written exclusively by the Go gateway
// (golang-migrate, migrations/0001_init.up.sql, masterplan §7). The frontend
// models them as Drizzle definitions ONLY to run hybrid direct-MySQL READS
// (§6.2) for SSR/loaders (dashboards, viewer, contacts). The frontend NEVER
// inserts/updates/deletes here — ownership is single-writer (§6.2).
//
// Conventions mirrored from 0001_init.up.sql:
//   - timestamps are epoch-ms, stored as BIGINT  -> bigint({ mode: "number" })
//   - VARCHAR(64) ULID PKs / VARCHAR ids
//   - BIGINT UNSIGNED AUTO_INCREMENT surrogate PKs -> bigint unsigned autoincrement
//   - JSON columns -> json()
//   - ownership by organization_id (a better-auth organization id)
//
// Verified by drizzle-kit introspect against the live gateway-migrated DB (R5
// smoke): this mirror matches the introspected truth column-for-column. If you
// change 0001_init.up.sql, re-introspect and re-mirror here.

import {
  bigint,
  index,
  int,
  json,
  mediumtext,
  mysqlEnum,
  mysqlTable,
  text,
  tinyint,
  uniqueIndex,
  varbinary,
  varchar,
} from "drizzle-orm/mysql-core";

// Registry of gateways — the central router's routing table. status/session_count/
// capacity are the lifecycle + load columns added by migration 0004
// (docs/specs/store.md). Mirrors the migrated DDL (manual sync; re-introspect when
// a live migrated DB is available).
export const gateways = mysqlTable("gateways", {
  id: varchar("id", { length: 64 }).primaryKey(),
  label: varchar("label", { length: 255 }),
  status: varchar("status", { length: 16 }).notNull().default("active"),
  sessionCount: int("session_count", { unsigned: true }).notNull().default(0),
  capacity: int("capacity", { unsigned: true }),
  baseUrl: text("base_url"),
  lastSeenAt: bigint("last_seen_at", { mode: "number" }),
  createdAt: bigint("created_at", { mode: "number" }).notNull(),
  updatedAt: bigint("updated_at", { mode: "number" }).notNull(),
});

export const waSessions = mysqlTable(
  "wa_sessions",
  {
    id: varchar("id", { length: 64 }).primaryKey(),
    organizationId: varchar("organization_id", { length: 64 }).notNull(),
    createdByUserId: varchar("created_by_user_id", { length: 64 }),
    gatewayId: varchar("gateway_id", { length: 64 }).notNull(),
    label: varchar("label", { length: 255 }),
    status: mysqlEnum("status", [
      "starting",
      "scan_qr_code",
      "working",
      "failed",
      "stopped",
      "logged_out",
    ])
      .notNull()
      .default("stopped"),
    waJid: varchar("wa_jid", { length: 255 }),
    waLid: varchar("wa_lid", { length: 255 }),
    phoneNumber: varchar("phone_number", { length: 64 }),
    isAdminSession: tinyint("is_admin_session").notNull().default(0),
    autoRead: tinyint("auto_read").notNull().default(1),
    presenceTyping: tinyint("presence_typing").notNull().default(0),
    ratePerMin: int("rate_per_min").notNull().default(20),
    ratePerHour: int("rate_per_hour").notNull().default(200),
    lastConnectedAt: bigint("last_connected_at", { mode: "number" }),
    createdAt: bigint("created_at", { mode: "number" }).notNull(),
    updatedAt: bigint("updated_at", { mode: "number" }).notNull(),
  },
  (t) => [
    index("idx_sessions_org").on(t.organizationId),
    index("idx_sessions_gateway").on(t.gatewayId),
    uniqueIndex("uq_sessions_jid").on(t.waJid),
  ],
);

export const webhooks = mysqlTable(
  "webhooks",
  {
    id: varchar("id", { length: 64 }).primaryKey(),
    organizationId: varchar("organization_id", { length: 64 }).notNull(),
    sessionId: varchar("session_id", { length: 64 }),
    url: text("url").notNull(),
    events: json("events").notNull(),
    hmacSecret: varbinary("hmac_secret", { length: 512 }),
    customHeaders: json("custom_headers"),
    retryPolicy: json("retry_policy").notNull(),
    active: tinyint("active").notNull().default(1),
    createdAt: bigint("created_at", { mode: "number" }).notNull(),
    updatedAt: bigint("updated_at", { mode: "number" }).notNull(),
  },
  (t) => [index("idx_webhooks_org").on(t.organizationId)],
);

export const webhookDeliveries = mysqlTable(
  "webhook_deliveries",
  {
    id: bigint("id", { mode: "number", unsigned: true })
      .autoincrement()
      .primaryKey(),
    webhookId: varchar("webhook_id", { length: 64 }).notNull(),
    eventId: varchar("event_id", { length: 64 }).notNull(),
    status: mysqlEnum("status", ["pending", "delivered", "failed", "dead"])
      .notNull()
      .default("pending"),
    attempts: int("attempts").notNull().default(0),
    responseCode: int("response_code"),
    nextRetryAt: bigint("next_retry_at", { mode: "number" }),
    lastError: text("last_error"),
    createdAt: bigint("created_at", { mode: "number" }).notNull(),
  },
  (t) => [index("idx_deliv_retry").on(t.status, t.nextRetryAt)],
);

// ===== Identity / Contacts model (global; not user-scoped) =====

export const whatsappIdentities = mysqlTable(
  "whatsapp_identities",
  {
    id: bigint("id", { mode: "number", unsigned: true })
      .autoincrement()
      .primaryKey(),
    lid: varchar("lid", { length: 255 }).notNull(),
    phoneNumber: varchar("phone_number", { length: 64 }),
    phoneJid: varchar("phone_jid", { length: 255 }),
    name: text("name"),
    businessName: text("business_name"),
    firstSeenAt: bigint("first_seen_at", { mode: "number" }).notNull(),
    updatedAt: bigint("updated_at", { mode: "number" }).notNull(),
  },
  (t) => [
    uniqueIndex("uq_identity_lid").on(t.lid),
    index("idx_identity_phone").on(t.phoneJid),
  ],
);

// NOTE: there is no whatsapp_contacts table. "Found users" is a projection over
// whatsapp_identities, with DM status derived from `chats` (type='dm') and group
// membership from whatsapp_group_members.

export const whatsappGroups = mysqlTable(
  "whatsapp_groups",
  {
    id: bigint("id", { mode: "number", unsigned: true })
      .autoincrement()
      .primaryKey(),
    groupJid: varchar("group_jid", { length: 255 }).notNull(),
    subject: text("subject"),
    description: text("description"),
    ownerJid: varchar("owner_jid", { length: 255 }),
    participantCount: int("participant_count"),
    isAnnounce: tinyint("is_announce"),
    isLocked: tinyint("is_locked"),
    createdAtWa: bigint("created_at_wa", { mode: "number" }),
    firstSeenAt: bigint("first_seen_at", { mode: "number" }).notNull(),
    updatedAt: bigint("updated_at", { mode: "number" }).notNull(),
  },
  (t) => [uniqueIndex("uq_group_jid").on(t.groupJid)],
);

export const whatsappGroupMembers = mysqlTable(
  "whatsapp_group_members",
  {
    id: bigint("id", { mode: "number", unsigned: true })
      .autoincrement()
      .primaryKey(),
    sessionId: varchar("session_id", { length: 64 }).notNull(),
    groupJid: varchar("group_jid", { length: 255 }).notNull(),
    lid: varchar("lid", { length: 255 }).notNull(),
    // per-group member tag (the second per-group identity beside the push name)
    tag: text("tag"),
    role: mysqlEnum("role", ["member", "admin", "superadmin"])
      .notNull()
      .default("member"),
    firstSeenAt: bigint("first_seen_at", { mode: "number" }).notNull(),
    lastSeenAt: bigint("last_seen_at", { mode: "number" }).notNull(),
  },
  (t) => [
    uniqueIndex("uq_group_member").on(t.sessionId, t.groupJid, t.lid),
    index("idx_gm_group").on(t.groupJid),
    index("idx_gm_lid").on(t.lid),
  ],
);

// ===== Messages & chats =====

export const chats = mysqlTable(
  "chats",
  {
    id: bigint("id", { mode: "number", unsigned: true })
      .autoincrement()
      .primaryKey(),
    sessionId: varchar("session_id", { length: 64 }).notNull(),
    chatJid: varchar("chat_jid", { length: 255 }).notNull(),
    type: mysqlEnum("type", [
      "dm",
      "group",
      "newsletter",
      "broadcast",
      "status",
    ]).notNull(),
    name: text("name"),
    lastMessageAt: bigint("last_message_at", { mode: "number" }),
    unreadCount: int("unread_count").notNull().default(0),
    archived: tinyint("archived").notNull().default(0),
    pinned: tinyint("pinned").notNull().default(0),
    mutedUntil: bigint("muted_until", { mode: "number" }),
  },
  (t) => [
    uniqueIndex("uq_chat").on(t.sessionId, t.chatJid),
    index("idx_chat_recent").on(t.sessionId, t.lastMessageAt),
  ],
);

export const messages = mysqlTable(
  "messages",
  {
    id: varchar("id", { length: 64 }).primaryKey(),
    sessionId: varchar("session_id", { length: 64 }).notNull(),
    waMessageId: varchar("wa_message_id", { length: 255 }).notNull(),
    chatJid: varchar("chat_jid", { length: 255 }).notNull(),
    senderLid: varchar("sender_lid", { length: 255 }),
    senderJid: varchar("sender_jid", { length: 255 }),
    fromMe: tinyint("from_me").notNull().default(0),
    direction: mysqlEnum("direction", ["in", "out"]).notNull(),
    type: varchar("type", { length: 32 }).notNull(),
    body: mediumtext("body"), // MEDIUMTEXT (confirmed by drizzle-kit introspect)
    quotedMessageId: varchar("quoted_message_id", { length: 255 }),
    mentions: json("mentions"),
    hasMedia: tinyint("has_media").notNull().default(0),
    mediaMeta: json("media_meta"),
    status: mysqlEnum("status", [
      "pending",
      "sent",
      "delivered",
      "read",
      "played",
      "failed",
    ]),
    ackLevel: int("ack_level"),
    error: text("error"),
    edited: tinyint("edited").notNull().default(0),
    deleted: tinyint("deleted").notNull().default(0),
    timestamp: bigint("timestamp", { mode: "number" }).notNull(),
    rawJson: json("raw_json"),
    createdAt: bigint("created_at", { mode: "number" }).notNull(),
  },
  (t) => [
    uniqueIndex("uq_msg").on(t.sessionId, t.waMessageId),
    index("idx_msg_chat").on(t.sessionId, t.chatJid, t.id),
    index("idx_msg_sender").on(t.senderLid),
  ],
);

export const polls = mysqlTable(
  "polls",
  {
    id: bigint("id", { mode: "number", unsigned: true })
      .autoincrement()
      .primaryKey(),
    sessionId: varchar("session_id", { length: 64 }).notNull(),
    pollMessageId: varchar("poll_message_id", { length: 255 }).notNull(),
    chatJid: varchar("chat_jid", { length: 255 }).notNull(),
    name: text("name"),
    options: json("options").notNull(),
    selectableCount: int("selectable_count").notNull().default(1),
    endTime: bigint("end_time", { mode: "number" }),
    hideVotes: tinyint("hide_votes").notNull().default(0),
    recapEmittedAt: bigint("recap_emitted_at", { mode: "number" }),
    createdAt: bigint("created_at", { mode: "number" }).notNull(),
    updatedAt: bigint("updated_at", { mode: "number" }).notNull(),
  },
  (t) => [uniqueIndex("uq_poll").on(t.sessionId, t.pollMessageId)],
);

export const pollVotes = mysqlTable(
  "poll_votes",
  {
    id: bigint("id", { mode: "number", unsigned: true })
      .autoincrement()
      .primaryKey(),
    sessionId: varchar("session_id", { length: 64 }).notNull(),
    pollMessageId: varchar("poll_message_id", { length: 255 }).notNull(),
    voterLid: varchar("voter_lid", { length: 255 }).notNull(),
    selectedOptions: json("selected_options").notNull(),
    timestamp: bigint("timestamp", { mode: "number" }).notNull(),
    rawJson: json("raw_json"),
  },
  (t) => [
    uniqueIndex("uq_pollvote_event").on(
      t.sessionId,
      t.pollMessageId,
      t.voterLid,
      t.timestamp,
    ),
    index("idx_pollvote").on(t.sessionId, t.pollMessageId),
  ],
);

export const outbox = mysqlTable(
  "outbox",
  {
    id: varchar("id", { length: 64 }).primaryKey(),
    organizationId: varchar("organization_id", { length: 64 }).notNull(),
    sessionId: varchar("session_id", { length: 64 }).notNull(),
    idempotencyKey: varchar("idempotency_key", { length: 255 }),
    payload: json("payload").notNull(),
    status: mysqlEnum("status", ["queued", "sending", "sent", "failed"])
      .notNull()
      .default("queued"),
    attempts: int("attempts").notNull().default(0),
    waMessageId: varchar("wa_message_id", { length: 255 }),
    error: text("error"),
    createdAt: bigint("created_at", { mode: "number" }).notNull(),
    updatedAt: bigint("updated_at", { mode: "number" }).notNull(),
  },
  (t) => [uniqueIndex("uq_idem").on(t.organizationId, t.idempotencyKey)],
);

export const eventLog = mysqlTable(
  "event_log",
  {
    id: bigint("id", { mode: "number", unsigned: true })
      .autoincrement()
      .primaryKey(),
    eventId: varchar("event_id", { length: 64 }).notNull(),
    organizationId: varchar("organization_id", { length: 64 }).notNull(),
    sessionId: varchar("session_id", { length: 64 }).notNull(),
    type: varchar("type", { length: 64 }).notNull(),
    payload: json("payload").notNull(),
    createdAt: bigint("created_at", { mode: "number" }).notNull(),
  },
  (t) => [
    index("idx_event_cursor").on(t.organizationId, t.sessionId, t.id),
    uniqueIndex("uq_event_id").on(t.eventId),
  ],
);
