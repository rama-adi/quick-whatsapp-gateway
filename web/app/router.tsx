// Router factory. TanStack Start calls getRouter() on both the server and the
// client to build a fresh router per request/load. We reuse the existing
// QueryClient factory (~/lib/query) and wire it into the router via the
// official SSR-query integration so loaders can prefetch and the cache
// dehydrates/hydrates across SSR.

import { createRouter as createTanStackRouter } from "@tanstack/react-router";
import { setupRouterSsrQueryIntegration } from "@tanstack/react-router-ssr-query";
import { routeTree } from "./routeTree.gen";
import { queryClient } from "~/lib/query";

export function getRouter() {
  const router = createTanStackRouter({
    routeTree,
    context: { queryClient },
    defaultPreload: "intent",
    // The QueryClient owns caching/staleness; let it decide refetch timing.
    defaultPreloadStaleTime: 0,
    scrollRestoration: true,
  });

  setupRouterSsrQueryIntegration({ router, queryClient });

  return router;
}

declare module "@tanstack/react-router" {
  interface Register {
    router: ReturnType<typeof getRouter>;
  }
}
