// Drizzle MySQL client (server-only).
//
// Single source of truth for DB access on the frontend server. better-auth
// OWNS the auth tables through this client (§6.2 single-writer); the WA app-data
// tables are GATEWAY-owned and modelled READ-ONLY in ./wa.ts.
//
// The pool is LAZY: we do NOT open a connection at import time. This keeps the
// module safe to import from the server graph without holding a socket open
// during build/SSR cold starts. §12 warns that direct reads from serverless
// functions can exhaust DB connections — use a pooler / PlanetScale / RDS Proxy
// in prod. The pool is created on first use.

import { drizzle, type MySql2Database } from "drizzle-orm/mysql2";
import mysql from "mysql2/promise";
import * as authSchema from "./auth-schema";
import * as waSchema from "./wa";

export const schema = { ...authSchema, ...waSchema };

let _pool: mysql.Pool | undefined;
let _db: MySql2Database<typeof schema> | undefined;

function getPool(): mysql.Pool {
  if (_pool) return _pool;
  const url = process.env.DATABASE_URL;
  if (!url) {
    throw new Error(
      "DATABASE_URL is not set — the frontend server needs it for better-auth + read-only WA queries.",
    );
  }
  _pool = mysql.createPool({ uri: url, connectionLimit: 10 });
  return _pool;
}

/**
 * The Drizzle client. Constructed lazily on first property access so importing
 * this module never opens a socket. Use for better-auth (via the adapter) and
 * read-only WA-data queries in loaders/server functions.
 */
export const db: MySql2Database<typeof schema> = new Proxy(
  {} as MySql2Database<typeof schema>,
  {
    get(_target, prop, receiver) {
      if (!_db) {
        _db = drizzle(getPool(), { schema, mode: "default" });
      }
      return Reflect.get(_db, prop, receiver);
    },
  },
);
