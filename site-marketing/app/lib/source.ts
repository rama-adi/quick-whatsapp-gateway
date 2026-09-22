// The content loader for the docs site. `docs` is the MDX collection compiled
// by fumadocs-mdx (see source.config.ts); openapi.loaderPlugin() lets the
// generated reference pages resolve their <APIPage> props from the spec at
// render time.
import { loader } from "fumadocs-core/source";
import { docs } from "collections/server";
import { openapi } from "./openapi";

export const source = loader({
  source: docs.toFumadocsSource(),
  baseUrl: "/docs",
  plugins: [openapi.loaderPlugin()],
});
