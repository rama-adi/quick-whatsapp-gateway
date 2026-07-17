import { readFile, readdir, rm, writeFile } from "node:fs/promises";
import { patchGeneratedSchema } from "./wa-introspection-patch.mts";

const directory = new URL("../app/lib/db/wa-generated/", import.meta.url);
const schema = new URL("schema.ts", directory);
const relations = new URL("relations.ts", directory);
const original = await readFile(schema, "utf8");
const once = patchGeneratedSchema(original);
// Reconstruct the known emitter defect and prove a second patch is identical.
const emitterShape = once.replace(", tinyint }", ", AnyMySqlColumn }");
if (patchGeneratedSchema(emitterShape) !== once) throw new Error("WA schema patch is not idempotent");
await writeFile(schema, once);
await writeFile(relations, `${(await readFile(relations, "utf8")).trimEnd()}\n`);
for (const entry of await readdir(directory)) if (entry.endsWith(".sql")) await rm(new URL(entry, directory));
await rm(new URL("meta", directory), { recursive: true, force: true });
