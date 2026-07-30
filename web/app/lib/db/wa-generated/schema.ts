import { mysqlTable, mysqlSchema, index, primaryKey, varchar, mysqlEnum, int, text, bigint, unique, json, check, mediumtext, tinyint } from "drizzle-orm/mysql-core"
import { sql } from "drizzle-orm"

export const backfillImports = mysqlTable("backfill_imports", {
	id: varchar({ length: 64 }).notNull(),
	sessionId: varchar("session_id", { length: 64 }).notNull(),
	organizationId: varchar("organization_id", { length: 64 }).notNull(),
	source: varchar({ length: 32 }).default('crypt15').notNull(),
	status: mysqlEnum(['running','succeeded','failed']).default('running').notNull(),
	chats: int().default(0).notNull(),
	messages: int().default(0).notNull(),
	identities: int().default(0).notNull(),
	groupsCount: int("groups_count").default(0).notNull(),
	groupMembers: int("group_members").default(0).notNull(),
	schemaFingerprint: varchar("schema_fingerprint", { length: 255 }),
	error: text(),
	createdAt: bigint("created_at", { mode: "number" }).notNull(),
	finishedAt: bigint("finished_at", { mode: "number" }),
},
(table) => [
	index("idx_backfill_session").on(table.sessionId, table.createdAt),
	primaryKey({ columns: [table.id], name: "backfill_imports_id"}),
]);

export const chats = mysqlTable("chats", {
	id: bigint({ mode: "number", unsigned: true }).autoincrement().notNull(),
	sessionId: varchar("session_id", { length: 64 }).notNull(),
	chatJid: varchar("chat_jid", { length: 255 }).notNull(),
	type: mysqlEnum(['dm','group','newsletter','broadcast','status']).notNull(),
	name: text(),
	lastMessageAt: bigint("last_message_at", { mode: "number" }),
	unreadCount: int("unread_count").default(0).notNull(),
	archived: tinyint().default(0).notNull(),
	pinned: tinyint().default(0).notNull(),
	mutedUntil: bigint("muted_until", { mode: "number" }),
},
(table) => [
	index("idx_chat_recent").on(table.sessionId, table.lastMessageAt),
	primaryKey({ columns: [table.id], name: "chats_id"}),
	unique("uq_chat").on(table.sessionId, table.chatJid),
]);

export const eventLog = mysqlTable("event_log", {
	id: bigint({ mode: "number", unsigned: true }).autoincrement().notNull(),
	eventId: varchar("event_id", { length: 64 }).notNull(),
	organizationId: varchar("organization_id", { length: 64 }).notNull(),
	sessionId: varchar("session_id", { length: 64 }).notNull(),
	type: varchar({ length: 64 }).notNull(),
	payload: json().notNull(),
	createdAt: bigint("created_at", { mode: "number" }).notNull(),
},
(table) => [
	index("idx_event_cursor").on(table.organizationId, table.sessionId, table.id),
	index("idx_event_retention").on(table.createdAt, table.id),
	primaryKey({ columns: [table.id], name: "event_log_id"}),
	unique("uq_event_id").on(table.eventId),
]);

export const gateways = mysqlTable("gateways", {
	id: varchar({ length: 64 }).notNull(),
	label: varchar({ length: 255 }),
	notes: text(),
	status: mysqlEnum(['pending_enrollment','joining','active','draining','drained','degraded','disabled']).default('pending_enrollment').notNull(),
	creatorKind: mysqlEnum("creator_kind", ['system','user']).default('system').notNull(),
	createdByUserId: varchar("created_by_user_id", { length: 64 }),
	baseUrl: text("base_url"),
	grpcEndpoint: varchar("grpc_endpoint", { length: 512 }),
	sessionCount: int("session_count", { unsigned: true }).default(0).notNull(),
	capacity: int({ unsigned: true }),
	desiredRevision: bigint("desired_revision", { mode: "number", unsigned: true }).notNull(),
	appliedRevision: bigint("applied_revision", { mode: "number", unsigned: true }).notNull(),
	softwareVersion: varchar("software_version", { length: 128 }),
	capabilities: json(),
	connectionEpoch: bigint("connection_epoch", { mode: "number", unsigned: true }).notNull(),
	enrolledAt: bigint("enrolled_at", { mode: "number" }),
	connectedAt: bigint("connected_at", { mode: "number" }),
	lastSeenAt: bigint("last_seen_at", { mode: "number" }),
	deletedAt: bigint("deleted_at", { mode: "number" }),
	createdAt: bigint("created_at", { mode: "number" }).notNull(),
	updatedAt: bigint("updated_at", { mode: "number" }).notNull(),
},
(table) => [
	index("idx_gateways_creator").on(table.createdByUserId),
	index("idx_gateways_revision").on(table.desiredRevision, table.appliedRevision),
	index("idx_gateways_status_seen").on(table.status, table.lastSeenAt),
	primaryKey({ columns: [table.id], name: "gateways_id"}),
	check("chk_gateway_creator", sql`(((\`creator_kind\` = _utf8mb4\'system\') and (\`created_by_user_id\` is null)) or ((\`creator_kind\` = _utf8mb4\'user\') and (\`created_by_user_id\` is not null)))`),
	check("chk_gateway_revisions", sql`(\`applied_revision\` <= \`desired_revision\`)`),
]);

export const messages = mysqlTable("messages", {
	id: varchar({ length: 64 }).notNull(),
	sessionId: varchar("session_id", { length: 64 }).notNull(),
	waMessageId: varchar("wa_message_id", { length: 255 }).notNull(),
	chatJid: varchar("chat_jid", { length: 255 }).notNull(),
	senderLid: varchar("sender_lid", { length: 255 }),
	senderJid: varchar("sender_jid", { length: 255 }),
	fromMe: tinyint("from_me").default(0).notNull(),
	direction: mysqlEnum(['in','out']).notNull(),
	type: varchar({ length: 32 }).notNull(),
	body: mediumtext(),
	quotedMessageId: varchar("quoted_message_id", { length: 255 }),
	mentions: json(),
	hasMedia: tinyint("has_media").default(0).notNull(),
	mediaMeta: json("media_meta"),
	status: mysqlEnum(['pending','sent','delivered','read','played','failed']),
	ackLevel: int("ack_level"),
	error: text(),
	edited: tinyint().default(0).notNull(),
	deleted: tinyint().default(0).notNull(),
	timestamp: bigint({ mode: "number" }).notNull(),
	rawJson: json("raw_json"),
	createdAt: bigint("created_at", { mode: "number" }).notNull(),
},
(table) => [
	index("idx_msg_chat").on(table.sessionId, table.chatJid, table.id),
	index("idx_msg_retention").on(table.timestamp, table.id),
	index("idx_msg_sender").on(table.senderLid),
	primaryKey({ columns: [table.id], name: "messages_id"}),
	unique("uq_msg").on(table.sessionId, table.waMessageId),
]);

export const outbox = mysqlTable("outbox", {
	id: varchar({ length: 64 }).notNull(),
	organizationId: varchar("organization_id", { length: 64 }).notNull(),
	sessionId: varchar("session_id", { length: 64 }).notNull(),
	idempotencyKey: varchar("idempotency_key", { length: 255 }),
	payload: json().notNull(),
	status: mysqlEnum(['queued','sending','sent','failed']).default('queued').notNull(),
	attempts: int().default(0).notNull(),
	waMessageId: varchar("wa_message_id", { length: 255 }),
	error: text(),
	createdAt: bigint("created_at", { mode: "number" }).notNull(),
	updatedAt: bigint("updated_at", { mode: "number" }).notNull(),
},
(table) => [
	primaryKey({ columns: [table.id], name: "outbox_id"}),
	unique("uq_idem").on(table.organizationId, table.idempotencyKey),
]);

export const pollVotes = mysqlTable("poll_votes", {
	id: bigint({ mode: "number", unsigned: true }).autoincrement().notNull(),
	sessionId: varchar("session_id", { length: 64 }).notNull(),
	pollMessageId: varchar("poll_message_id", { length: 255 }).notNull(),
	voterLid: varchar("voter_lid", { length: 255 }).notNull(),
	selectedOptions: json("selected_options").notNull(),
	timestamp: bigint({ mode: "number" }).notNull(),
	rawJson: json("raw_json"),
},
(table) => [
	index("idx_pollvote").on(table.sessionId, table.pollMessageId),
	primaryKey({ columns: [table.id], name: "poll_votes_id"}),
	unique("uq_pollvote_event").on(table.sessionId, table.pollMessageId, table.voterLid, table.timestamp),
]);

export const polls = mysqlTable("polls", {
	id: bigint({ mode: "number", unsigned: true }).autoincrement().notNull(),
	sessionId: varchar("session_id", { length: 64 }).notNull(),
	pollMessageId: varchar("poll_message_id", { length: 255 }).notNull(),
	chatJid: varchar("chat_jid", { length: 255 }).notNull(),
	name: text(),
	options: json().notNull(),
	selectableCount: int("selectable_count").default(1).notNull(),
	endTime: bigint("end_time", { mode: "number" }),
	hideVotes: tinyint("hide_votes").default(0).notNull(),
	recapEmittedAt: bigint("recap_emitted_at", { mode: "number" }),
	createdAt: bigint("created_at", { mode: "number" }).notNull(),
	updatedAt: bigint("updated_at", { mode: "number" }).notNull(),
},
(table) => [
	index("idx_poll_recap_due").on(table.endTime, table.recapEmittedAt),
	primaryKey({ columns: [table.id], name: "polls_id"}),
	unique("uq_poll").on(table.sessionId, table.pollMessageId),
]);

export const waSessions = mysqlTable("wa_sessions", {
	id: varchar({ length: 64 }).notNull(),
	organizationId: varchar("organization_id", { length: 64 }).notNull(),
	createdByUserId: varchar("created_by_user_id", { length: 64 }),
	gatewayId: varchar("gateway_id", { length: 64 }).notNull(),
	label: varchar({ length: 255 }),
	status: mysqlEnum(['starting','scan_qr_code','working','failed','stopped','logged_out']).default('stopped').notNull(),
	waJid: varchar("wa_jid", { length: 255 }),
	waLid: varchar("wa_lid", { length: 255 }),
	phoneNumber: varchar("phone_number", { length: 64 }),
	isAdminSession: tinyint("is_admin_session").default(0).notNull(),
	autoRead: tinyint("auto_read").default(1).notNull(),
	presenceTyping: tinyint("presence_typing").default(0).notNull(),
	ratePerMin: int("rate_per_min").default(20).notNull(),
	ratePerHour: int("rate_per_hour").default(200).notNull(),
	lastConnectedAt: bigint("last_connected_at", { mode: "number" }),
	createdAt: bigint("created_at", { mode: "number" }).notNull(),
	updatedAt: bigint("updated_at", { mode: "number" }).notNull(),
},
(table) => [
	index("idx_sessions_gateway").on(table.gatewayId),
	index("idx_sessions_org").on(table.organizationId),
	primaryKey({ columns: [table.id], name: "wa_sessions_id"}),
	unique("uq_sessions_jid").on(table.waJid),
]);

export const webhookDeliveries = mysqlTable("webhook_deliveries", {
	id: bigint({ mode: "number", unsigned: true }).autoincrement().notNull(),
	webhookId: varchar("webhook_id", { length: 64 }).notNull(),
	eventId: varchar("event_id", { length: 64 }).notNull(),
	status: mysqlEnum(['pending','delivered','failed','dead']).default('pending').notNull(),
	attempts: int().default(0).notNull(),
	responseCode: int("response_code"),
	nextRetryAt: bigint("next_retry_at", { mode: "number" }),
	lastError: text("last_error"),
	createdAt: bigint("created_at", { mode: "number" }).notNull(),
},
(table) => [
	index("idx_deliv_event_status").on(table.eventId, table.status),
	index("idx_deliv_retention").on(table.status, table.createdAt, table.id),
	index("idx_deliv_retry").on(table.status, table.nextRetryAt),
	primaryKey({ columns: [table.id], name: "webhook_deliveries_id"}),
	unique("uq_deliv_webhook_event").on(table.webhookId, table.eventId),
]);

export const whatsappGroupMembers = mysqlTable("whatsapp_group_members", {
	id: bigint({ mode: "number", unsigned: true }).autoincrement().notNull(),
	sessionId: varchar("session_id", { length: 64 }).notNull(),
	groupJid: varchar("group_jid", { length: 255 }).notNull(),
	lid: varchar({ length: 255 }).notNull(),
	tag: text(),
	role: mysqlEnum(['member','admin','superadmin']).default('member').notNull(),
	firstSeenAt: bigint("first_seen_at", { mode: "number" }).notNull(),
	lastSeenAt: bigint("last_seen_at", { mode: "number" }).notNull(),
},
(table) => [
	index("idx_gm_group").on(table.groupJid),
	index("idx_gm_lid").on(table.lid),
	primaryKey({ columns: [table.id], name: "whatsapp_group_members_id"}),
	unique("uq_group_member").on(table.sessionId, table.groupJid, table.lid),
]);

export const whatsappGroups = mysqlTable("whatsapp_groups", {
	id: bigint({ mode: "number", unsigned: true }).autoincrement().notNull(),
	groupJid: varchar("group_jid", { length: 255 }).notNull(),
	subject: text(),
	description: text(),
	ownerJid: varchar("owner_jid", { length: 255 }),
	participantCount: int("participant_count"),
	isAnnounce: tinyint("is_announce"),
	isLocked: tinyint("is_locked"),
	createdAtWa: bigint("created_at_wa", { mode: "number" }),
	firstSeenAt: bigint("first_seen_at", { mode: "number" }).notNull(),
	updatedAt: bigint("updated_at", { mode: "number" }).notNull(),
},
(table) => [
	primaryKey({ columns: [table.id], name: "whatsapp_groups_id"}),
	unique("uq_group_jid").on(table.groupJid),
]);

export const whatsappIdentities = mysqlTable("whatsapp_identities", {
	id: bigint({ mode: "number", unsigned: true }).autoincrement().notNull(),
	lid: varchar({ length: 255 }).notNull(),
	phoneNumber: varchar("phone_number", { length: 64 }),
	phoneJid: varchar("phone_jid", { length: 255 }),
	name: text(),
	businessName: text("business_name"),
	firstSeenAt: bigint("first_seen_at", { mode: "number" }).notNull(),
	updatedAt: bigint("updated_at", { mode: "number" }).notNull(),
},
(table) => [
	index("idx_identity_phone").on(table.phoneJid),
	primaryKey({ columns: [table.id], name: "whatsapp_identities_id"}),
	unique("uq_identity_lid").on(table.lid),
]);
