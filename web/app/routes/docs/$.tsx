// Catch-all docs route. The server loader resolves the page from the slug and
// serializes the page tree; the client loader renders the compiled MDX. This
// is self-contained — it does not touch the authed /_app surface or its shell.
//
// OpenAPI reference pages (generated under content/docs/api) need the bundled
// spec to render their <APIPage>. The server loader preloads it for those
// pages and the client loader binds it onto the APIPage component, so the
// generated `<APIPage document=… operations=… />` resolves the schema from the
// `preloaded` payload instead of trying to fetch the spec at runtime.
import { createFileRoute, notFound } from "@tanstack/react-router";
import { DocsLayout } from "fumadocs-ui/layouts/docs";
import { createServerFn } from "@tanstack/react-start";
// `~/lib/source` and `~/lib/openapi` pull in the fumadocs-mdx server runtime
// (which uses node:path). They are imported for VALUES only inside the server
// handler below (via dynamic import) so they never enter the client bundle —
// a top-level value import here leaks node:path into the browser and breaks
// hydration app-wide. The type-only import is erased at build, so it is safe.
import type * as SourceModule from "~/lib/source";
import browserCollections from "collections/browser";
import {
  DocsBody,
  DocsDescription,
  DocsPage,
  DocsTitle,
} from "fumadocs-ui/layouts/docs/page";
import { APIPage } from "~/components/openapi-page";
import { baseOptions } from "~/lib/layout.shared";
import { useFumadocsLoader } from "fumadocs-core/source/client";
import { Suspense } from "react";
import { useMDXComponents } from "~/components/mdx";
import type { OpenAPIPageProps_Preloaded } from "fumadocs-openapi/ui";

// The bundled-spec payload <APIPage> needs to render an OpenAPI page without a
// runtime fetch. Its `docs` values are OpenAPI `Document`s — plain JSON that
// serializes fine over the server-fn boundary, but the Document type carries
// `unknown`-typed fields the TanStack serializer can't prove are serializable.
type PreloadedDocs = OpenAPIPageProps_Preloaded["preloaded"];

// The generated OpenAPI MDX renders `<APIPage document=… operations=… />`; the
// wrapper below forwards those props and adds the preloaded spec.
type GeneratedAPIPageProps = Omit<OpenAPIPageProps_Preloaded, "preloaded">;

type SerializedPageTree = Awaited<
  ReturnType<typeof SourceModule.source.serializePageTree>
>;

type LoaderData = {
  path: string;
  preloaded: PreloadedDocs | null;
  pageTree: SerializedPageTree;
};

export const Route = createFileRoute("/docs/$")({
  component: Page,
  loader: async ({ params }) => {
    const slugs = params._splat?.split("/") ?? [];
    const data = await serverLoader({ data: slugs });
    await clientLoader.preload(data.path);
    return data;
  },
});

const serverLoader = createServerFn({ method: "GET" })
  .validator((slugs: string[]) => slugs)
  .handler(async ({ data: slugs }): Promise<LoaderData> => {
    // Server-only: imported here (not at module scope) so node:path stays out
    // of the client bundle.
    const { source } = await import("~/lib/source");
    const { openapi } = await import("~/lib/openapi");
    const page = source.getPage(slugs);
    if (!page) throw notFound();

    // Generated OpenAPI pages carry a `_openapi` meta; preload the bundled
    // spec so <APIPage> can render without a runtime fetch.
    const isOpenAPI = "_openapi" in page.data;
    const preloaded = isOpenAPI
      ? (await openapi.preloadOpenAPIPage(page)).preloaded
      : null;

    return {
      path: page.path,
      preloaded,
      pageTree: await source.serializePageTree(source.getPageTree()),
      // The Document JSON is serializable at runtime; only the compile-time
      // serializer check is over-strict, so we assert the wire shape here.
    } as LoaderData;
  });

const clientLoader = browserCollections.docs.createClientLoader({
  component(
    { toc, frontmatter, default: MDX },
    { preloaded }: { preloaded: PreloadedDocs | null },
  ) {
    // For OpenAPI pages, hand <APIPage> the preloaded spec; the generated MDX
    // passes the matching `document`/`operations`.
    const components = useMDXComponents(
      preloaded
        ? {
            APIPage: (props: GeneratedAPIPageProps) => (
              <APIPage {...props} preloaded={preloaded} />
            ),
          }
        : undefined,
    );
    return (
      <DocsPage toc={toc}>
        <DocsTitle>{frontmatter.title}</DocsTitle>
        <DocsDescription>{frontmatter.description}</DocsDescription>
        <DocsBody>
          <MDX components={components} />
        </DocsBody>
      </DocsPage>
    );
  },
});

function Page() {
  const data = useFumadocsLoader(Route.useLoaderData());

  return (
    <DocsLayout {...baseOptions()} tree={data.pageTree}>
      <Suspense>
        {clientLoader.useContent(data.path, { preloaded: data.preloaded })}
      </Suspense>
    </DocsLayout>
  );
}
