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
import { RootProvider } from "fumadocs-ui/provider/tanstack";
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
import docsCss from "~/docs.css?url";

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
  head: () => ({
    links: [{ rel: "stylesheet", href: docsCss }],
  }),
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

    // SHOW_CONTRIB_WIKI gates the developer ("Contributing") docs under
    // /docs/dev. Default: shown. Set SHOW_CONTRIB_WIKI=false in prod to hide
    // the section from the nav AND 404 its pages.
    const showContrib = process.env.SHOW_CONTRIB_WIKI !== "false";
    if (!showContrib && slugs[0] === "dev") throw notFound();

    const page = source.getPage(slugs);
    if (!page) throw notFound();

    // Generated OpenAPI pages carry a `_openapi` meta; preload the bundled
    // spec so <APIPage> can render without a runtime fetch.
    const isOpenAPI = "_openapi" in page.data;
    const preloaded = isOpenAPI
      ? withRuntimeOpenAPIServer((await openapi.preloadOpenAPIPage(page)).preloaded)
      : null;

    const tree = source.getPageTree();
    const visibleTree = showContrib
      ? tree
      : { ...tree, children: tree.children.filter((n) => !isContribNode(n)) };

    return {
      path: page.path,
      preloaded,
      pageTree: await source.serializePageTree(visibleTree),
      // The Document JSON is serializable at runtime; only the compile-time
      // serializer check is over-strict, so we assert the wire shape here.
    } as LoaderData;
  });

function withRuntimeOpenAPIServer(preloaded: PreloadedDocs): PreloadedDocs {
  const apiBaseURL = runtimeOpenAPIBaseURL();
  if (!apiBaseURL) return preloaded;

  return {
    ...preloaded,
    docs: Object.fromEntries(
      Object.entries(preloaded.docs).map(([key, doc]) => [
        key,
        {
          ...doc,
          servers: [
            {
              description: "Configured API base URL",
              url: apiBaseURL,
            },
          ],
        },
      ]),
    ),
  };
}

function runtimeOpenAPIBaseURL(): string | null {
  const origin = process.env.VITE_GATEWAY_URL?.replace(/\/+$/, "");
  return origin ? `${origin}/api/v1` : null;
}

// A page-tree node belongs to the developer wiki if it (or any descendant)
// lives under /docs/dev. Used to drop that folder from the nav when
// SHOW_CONTRIB_WIKI=false.
function isContribNode(node: unknown): boolean {
  const n = node as {
    url?: string;
    index?: { url?: string };
    children?: unknown[];
  };
  const underDev = (url?: string) =>
    url === "/docs/dev" || (url?.startsWith("/docs/dev/") ?? false);
  if (underDev(n.url) || underDev(n.index?.url)) return true;
  return Array.isArray(n.children) && n.children.some(isContribNode);
}

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
    <RootProvider>
      <DocsLayout {...baseOptions()} tree={data.pageTree}>
        <Suspense>
          {clientLoader.useContent(data.path, { preloaded: data.preloaded })}
        </Suspense>
      </DocsLayout>
    </RootProvider>
  );
}
