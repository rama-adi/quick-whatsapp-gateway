function identifier(value: string): string | null {
  const trimmed = value.trim();
  if (/^`(?:``|[^`])+`$/.test(trimmed)) return trimmed.slice(1, -1).replaceAll("``", "`");
  if (/^[A-Za-z0-9_$]+$/.test(trimmed)) return trimmed;
  return null;
}

/** Accept only USAGE globally plus one SELECT grant for every exact allowed table. */
export function validateWAIntrospectionGrants(currentDB: string, allowedTables: ReadonlySet<string>, grantRows: readonly string[]): void {
  const database = currentDB.toLocaleLowerCase("en-US");
  const allowed = new Set([...allowedTables].map((table) => table.toLocaleLowerCase("en-US")));
  const seen = new Set<string>();
  let usage = 0;

  for (const raw of grantRows) {
    const grant = raw.trim();
    if (/^GRANT\s+USAGE\s+ON\s+\*\.\*\s+TO\s+\S+$/i.test(grant)) {
      usage++;
      continue;
    }
    const match = grant.match(/^GRANT\s+SELECT\s+ON\s+(.+)\s+TO\s+\S+$/i);
    if (!match?.[1]) throw new Error(`unsafe introspection grant: ${grant}`);
    const target = match[1].match(/^(.+)\.(.+)$/);
    if (!target?.[1] || !target[2]) throw new Error(`invalid introspection grant target: ${grant}`);
    const targetDB = identifier(target[1]);
    const table = identifier(target[2]);
    if (!targetDB || !table || targetDB.toLocaleLowerCase("en-US") !== database) throw new Error(`cross-database or invalid introspection grant: ${grant}`);
    const normalizedTable = table.toLocaleLowerCase("en-US");
    if (!allowed.has(normalizedTable)) throw new Error(`non-allowlisted introspection grant: ${grant}`);
    if (seen.has(normalizedTable)) throw new Error(`duplicate introspection grant: ${grant}`);
    seen.add(normalizedTable);
  }
  if (usage !== 1) throw new Error(`expected exactly one global USAGE grant, found ${usage}`);
  const missing = [...allowed].filter((table) => !seen.has(table));
  if (missing.length) throw new Error(`missing table SELECT grants: ${missing.join(",")}`);
}
