import { rm } from "node:fs/promises";
import mysql from "mysql2/promise";
import { validateWAIntrospectionGrants } from "./wa-introspection-grants.mts";

const allowed = new Set(["backfill_imports", "chats", "event_log", "gateways", "messages", "outbox", "poll_votes", "polls", "wa_sessions", "webhook_deliveries", "whatsapp_group_members", "whatsapp_groups", "whatsapp_identities"]);
const excluded = new Set(["audit_events", "gateway_certificates", "gateway_enrollment_tokens", "pki_authorities", "pki_rotation_lock", "webhooks", "media_buckets", "session_media_storage", "media_assets", "outgoing_message_event_claims"]);
const url = process.env.WA_INTROSPECTION_DATABASE_URL;
if (!url) throw new Error("WA_INTROSPECTION_DATABASE_URL is required");
const db = await mysql.createConnection(url);
try {
  const [grantRows] = await db.query<Record<string, string>[]>("SHOW GRANTS");
  const grants = grantRows.flatMap((row) => Object.values(row));
  validateWAIntrospectionGrants((await db.query<{ db: string }[]>("SELECT DATABASE() AS db"))[0][0]?.db ?? "", allowed, grants);
  const [rows] = await db.query<{ TABLE_NAME: string }[]>("SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() ORDER BY TABLE_NAME");
  const visible = new Set(rows.map((row) => row.TABLE_NAME));
  for (const table of excluded) if (visible.has(table)) throw new Error(`excluded control-plane table is visible: ${table}`);
  const missing = [...allowed].filter((table) => !visible.has(table));
  const extra = [...visible].filter((table) => !allowed.has(table));
  if (missing.length || extra.length) throw new Error(`introspection visibility mismatch; missing=${missing.join(",")} extra=${extra.join(",")}`);
} finally { await db.end(); }
await rm(new URL("../app/lib/db/wa-generated", import.meta.url), { recursive: true, force: true });
