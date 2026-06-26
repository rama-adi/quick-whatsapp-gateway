// Regenerate the OpenAPI reference MDX under content/docs/api from the gateway
// contract (../docs/openapi.yaml, via app/lib/openapi.ts). Run with:
//   pnpm docs:openapi
// One MDX page per operation, foldered by tag. Commit the result so the docs
// site builds on a fresh checkout without a generate step.
import { generateFiles } from "fumadocs-openapi";
import { createOpenAPI } from "fumadocs-openapi/server";
import { rm } from "node:fs/promises";

// Same spec + server config as app/lib/openapi.ts. Kept inline so this script
// runs under `node --experimental-strip-types` without a cross-module
// extension-resolution step (the runtime <APIPage> uses app/lib/openapi.ts).
const openapi = createOpenAPI({ input: ["../docs/openapi.yaml"] });

const OUT = "./content/docs/api";

// Clean first so renamed/removed operations don't leave orphan pages behind.
await rm(OUT, { recursive: true, force: true });

await generateFiles({
  input: openapi,
  output: OUT,
  per: "operation",
  groupBy: "tag",
  // Write meta.json per tag folder so the generated reference shows up as an
  // ordered tree in the docs sidebar.
  meta: true,
  // Add a landing page for the section and give the root meta a title +
  // index entry. Done here (not by hand) so a regen never clobbers them.
  beforeWrite(files) {
    files.push({
      path: "index.mdx",
      content: `---
title: API reference
description: Every gateway endpoint, generated from the OpenAPI contract.
---

These pages are generated from the gateway's OpenAPI contract, so they always
match the deployed API. Each endpoint shows its request and response shapes and
an interactive request panel.

New here? The [guides](/docs/guides) walk through pairing and sending first.
`,
    });

    const meta = files.find((f) => f.path === "meta.json");
    if (meta) {
      const parsed = JSON.parse(meta.content) as { pages: string[] };
      meta.content = JSON.stringify(
        { title: "API reference", pages: ["index", ...parsed.pages] },
        null,
        2,
      );
    }
  },
});
