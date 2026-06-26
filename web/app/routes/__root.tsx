// Root route: the HTML document shell + global providers.
// Ported from the v1 app/root.tsx (react-router) to TanStack Start idioms:
//   - <Layout> document  -> shellComponent (HeadContent / Scripts)
//   - links()            -> head().links (fonts) + the app.css stylesheet
//   - QueryClientProvider -> wraps the outlet; the same queryClient instance is
//     carried in the router context (see app/router.tsx) so loaders share it.
//   - ErrorBoundary       -> errorComponent

import {
  HeadContent,
  Outlet,
  Scripts,
  createRootRouteWithContext,
} from "@tanstack/react-router";
import { QueryClientProvider, type QueryClient } from "@tanstack/react-query";
import { RootProvider } from "fumadocs-ui/provider/tanstack";
import { queryClient } from "~/lib/query";
import { Toaster } from "~/components/ui/sonner";
import appCss from "~/app.css?url";

export interface RouterContext {
  queryClient: QueryClient;
}

export const Route = createRootRouteWithContext<RouterContext>()({
  head: () => ({
    meta: [
      { charSet: "utf-8" },
      { name: "viewport", content: "width=device-width, initial-scale=1" },
      { title: "WA Gateway" },
    ],
    links: [
      { rel: "preconnect", href: "https://fonts.googleapis.com" },
      {
        rel: "preconnect",
        href: "https://fonts.gstatic.com",
        crossOrigin: "anonymous",
      },
      {
        rel: "stylesheet",
        href: "https://fonts.googleapis.com/css2?family=Inter:ital,opsz,wght@0,14..32,100..900;1,14..32,100..900&display=swap",
      },
      { rel: "stylesheet", href: appCss },
    ],
  }),
  shellComponent: RootDocument,
  errorComponent: RootErrorBoundary,
});

function RootDocument({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <head>
        <HeadContent />
      </head>
      <body>
        {/* RootProvider gives the docs site its theme + search-dialog context;
            it wraps the whole tree so /docs renders correctly while the app
            routes inside keep their QueryClientProvider untouched. */}
        <RootProvider>
          <QueryClientProvider client={queryClient}>
            {children}
            <Toaster richColors position="top-right" />
          </QueryClientProvider>
        </RootProvider>
        <Scripts />
      </body>
    </html>
  );
}

function RootErrorBoundary({ error }: { error: unknown }) {
  const message = error instanceof Error ? error.message : "An unexpected error occurred.";
  const stack = import.meta.env.DEV && error instanceof Error ? error.stack : undefined;
  return (
    <main className="container mx-auto p-4 pt-16">
      <h1 className="text-lg font-semibold">Oops!</h1>
      <p className="text-sm text-muted-foreground">{message}</p>
      {stack && (
        <pre className="w-full overflow-x-auto p-4 text-xs">
          <code>{stack}</code>
        </pre>
      )}
    </main>
  );
}
