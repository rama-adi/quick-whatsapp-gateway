import { generateFiles } from "fumadocs-openapi";
import { createOpenAPI } from "fumadocs-openapi/server";
import { rm } from "node:fs/promises";

const output = "./content/docs/api";
await rm(output, { recursive: true, force: true });
await generateFiles({
  input: createOpenAPI({ input: ["../docs/openapi.yaml"] }),
  output,
  per: "operation",
  groupBy: "tag",
  includeDescription: true,
  meta: true,
  beforeWrite(files) {
    files.push({ path: "index.mdx", content: `---
title: Complete API reference
description: Request and response contracts for application integrations and platform administration.
---

This reference is generated from \`docs/openapi.yaml\` in the
[WhatsApp Gateway source repository](https://github.com/rama-adi/quick-whatsapp-gateway).
It describes the source revision used to build this documentation website.
The running API serves its own contract at \`/api/v1/openapi.yaml\`.

Choose an endpoint in the sidebar for its request, response, permissions, and errors.
The **Admin** and **Gateway Administration** sections are for installation operators.
User-facing documentation in the dashboard omits those platform operations.

For authentication and ownership, read the [trust model](/docs/architecture/trust-model).
For webhook and realtime payloads, read [events](/docs/architecture/eventing).
For protocol boundaries, read [gRPC contracts](/docs/architecture/grpc-contracts).

API acceptance confirms dispatch. Verify experimental message rendering on the
receiving clients; each operation documents its compatibility requirements.
` });
    const meta = files.find((file) => file.path === "meta.json");
    if (meta) {
      const data = JSON.parse(meta.content);
      meta.content = JSON.stringify({ ...data, title: "API reference", pages: ["index", ...data.pages] }, null, 2);
    }
  },
});
