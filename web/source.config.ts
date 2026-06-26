// fumadocs-mdx content config. The fumadocs-mdx Vite plugin reads this to
// compile MDX under content/docs into the generated `.source/` collection
// (aliased as `collections/*` in tsconfig). One `docs` collection backs the
// whole docs site — the user guides and the generated OpenAPI reference pages.
import { defineConfig, defineDocs } from "fumadocs-mdx/config";

export const docs = defineDocs({
  dir: "content/docs",
});

export default defineConfig();
