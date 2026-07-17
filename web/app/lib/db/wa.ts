// READ-ONLY mirror of the API-owned WA operational schema.
// Regenerate only with `pnpm db:introspect:wa` using a per-table SELECT-only
// MySQL account; the API migration account remains the sole writer.
export * from "./wa-generated/schema";
