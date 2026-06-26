// better-auth catch-all route handler — mounts the auth API at /api/auth/*.
//
// TanStack Start server route (verified against @tanstack/react-start@1.168 /
// start-server-core@1.169): a route file with `server.handlers.<METHOD>` is
// matched and invoked by createStartHandler. We delegate every method to
// better-auth's web-standard handler (Request -> Response), which serves
// sign-in/up, JWKS (/api/auth/jwks), token mint (/api/auth/token), admin, org,
// api-key, two-factor, etc.
//
// The `.$` splat in the filename makes this match /api/auth and everything
// nested under it.

import { createFileRoute } from "@tanstack/react-router";
import { auth } from "~/lib/auth/server";

export const Route = createFileRoute("/api/auth/$")({
  server: {
    handlers: {
      ANY: ({ request }) => auth.handler(request),
    },
  },
});
