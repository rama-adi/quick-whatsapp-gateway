// drizzle-kit config (§6.2). Drives:
//   - `drizzle-kit migrate` — applies the BETTER-AUTH-owned auth tables (the
//     frontend is their single writer). The WA app-data tables are GATEWAY-owned
//     (Go golang-migrate) and must NOT be migrated from here.
//   - `drizzle-kit introspect` — regenerates app/lib/db/wa.ts (the READ-ONLY WA
//     mirror) from the live, gateway-migrated DB. Needs a live DB → DEFERRED to
//     the R5 smoke; the hand-written wa.ts mirror stands in meanwhile.
//
// `schema` points at the whole db dir, but only auth-schema.ts is the frontend's
// to migrate. Generate auth migrations narrowly and review the diff so a stray
// WA-table statement never ships (the WA tables already exist via the Go migration).

import { defineConfig } from "drizzle-kit";

export default defineConfig({
  dialect: "mysql",
  schema: "./app/lib/db/auth-schema.ts",
  out: "./drizzle",
  dbCredentials: {
    url: process.env.DATABASE_URL ?? "",
  },
  // Keep introspect/push from touching tables we don't own. (drizzle-kit reads
  // tablesFilter for introspect; auth tables are the frontend's writable set.)
  tablesFilter: [
    "user",
    "session",
    "account",
    "verification",
    "twoFactor",
    "two_factor",
    "apikey",
    "jwks",
    "organization",
    "member",
    "invitation",
  ],
});
