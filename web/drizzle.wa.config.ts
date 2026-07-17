// Read-only schema introspection for gateway-owned WA operational tables.
// The database account used here must have SELECT only; tablesFilter is a
// reproducibility guard, not the security boundary.
import { defineConfig } from "drizzle-kit";

export default defineConfig({
  dialect: "mysql",
  out: "./app/lib/db/wa-generated",
  casing: "camelCase",
  dbCredentials: { url: process.env.WA_INTROSPECTION_DATABASE_URL ?? (() => { throw new Error("WA_INTROSPECTION_DATABASE_URL is required") })() },
  tablesFilter: [
    "gateways", "wa_sessions", "whatsapp_identities", "whatsapp_groups",
    "whatsapp_group_members", "chats", "messages", "polls", "poll_votes",
    "webhook_deliveries", "outbox",
    "event_log", "backfill_imports",
  ],
});
