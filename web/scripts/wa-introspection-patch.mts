const coreImport = /import \{([^}]*)\} from "drizzle-orm\/mysql-core"/;

export function patchGeneratedSchema(input: string): string {
  const matches = [...input.matchAll(new RegExp(coreImport.source, "g"))];
  if (matches.length !== 1) throw new Error(`expected one mysql-core import, found ${matches.length}`);
  const match = matches[0];
  if (!match?.[1]) throw new Error("mysql-core import did not contain a named import list");
  const anyCount = (input.match(/\bAnyMySqlColumn\b/g) ?? []).length;
  if (anyCount !== 1) throw new Error(`expected one unused AnyMySqlColumn emitter import, found ${anyCount}`);
  const tinyCalls = (input.match(/\btinyint\s*\(/g) ?? []).length;
  if (tinyCalls === 0) throw new Error("expected emitted tinyint calls");

  let names = match[1].split(",").map((name) => name.trim()).filter(Boolean);
  if (!names.includes("AnyMySqlColumn")) throw new Error("AnyMySqlColumn appeared outside the emitter import");
  names = names.filter((name) => name !== "AnyMySqlColumn");
  if (!names.includes("tinyint")) names.push("tinyint");
  if (names.filter((name) => name === "tinyint").length !== 1) throw new Error("tinyint import is not unique");
  let output = input.replace(coreImport, `import { ${names.join(", ")} } from "drizzle-orm/mysql-core"`);
  output = output.replace(/\(table\) => \[\n([\s\S]*?)\n\]\);/g, (block, body: string) => {
    const lines = body.split("\n");
    const indexes = lines.filter((line) => /^\tindex\(/.test(line)).sort();
    const rest = lines.filter((line) => !/^\tindex\(/.test(line));
    return `(table) => [\n${[...indexes, ...rest].join("\n")}\n]);`;
  });
  if (/\bAnyMySqlColumn\b/.test(output)) throw new Error("AnyMySqlColumn remains after patch");
  if ((output.match(/\btinyint\b/g) ?? []).length !== tinyCalls + 1) throw new Error("unexpected final tinyint import/call count");
  return output;
}
