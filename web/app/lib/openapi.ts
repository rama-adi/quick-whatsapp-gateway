// OpenAPI server for fumadocs-openapi. It reads the gateway's contract of
// record (../docs/openapi.yaml, same file the gen:api script consumes) and
// drives two things:
//   - the docs:openapi script (generateFiles) that writes the reference MDX
//     under content/docs/api, and
//   - the runtime <APIPage> render via openapi.loaderPlugin() in lib/source.ts.
import { createOpenAPI } from "fumadocs-openapi/server";

export const openapi = createOpenAPI({
  input: ["../docs/openapi.yaml"],
});
