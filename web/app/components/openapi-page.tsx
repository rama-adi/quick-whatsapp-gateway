// The <APIPage> client component the generated OpenAPI reference MDX renders.
// fumadocs-openapi builds it once here; mdx.tsx exposes it under the name the
// generated pages import ("APIPage").
import { createOpenAPIPage } from "fumadocs-openapi/ui";

export const APIPage = createOpenAPIPage({
  shikiOptions: {
    themes: {
      light: "github-light",
      dark: "github-dark",
    },
  },
  schemaUI: {
    showExample: true,
  },
  playground: {
    enabled: true,
  },
});
