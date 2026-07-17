import { describe, expect, it } from "vitest";
import { patchGeneratedSchema } from "../../../scripts/wa-introspection-patch.mts";

describe("WA introspection emitter patch", () => {
  const emitted = 'import { mysqlTable, AnyMySqlColumn, varbinary } from "drizzle-orm/mysql-core"\nconst x = tinyint()\n';
  it("is exact and stable across reconstructed repeated generation", () => {
    const once = patchGeneratedSchema(emitted);
    expect(once).not.toContain("AnyMySqlColumn");
    expect(once.match(/tinyint/g)).toHaveLength(2);
    expect(patchGeneratedSchema(once.replace(", tinyint }", ", AnyMySqlColumn }"))).toBe(once);
  });
  it("fails loudly on an unexpected emitter shape", () => {
    expect(() => patchGeneratedSchema('import { mysqlTable } from "drizzle-orm/mysql-core"')).toThrow();
  });
});
