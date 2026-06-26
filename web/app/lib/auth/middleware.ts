// Server middleware for authed server functions (§12). Resolves the better-auth
// session once and attaches a typed {user, activeOrg, role} context, redirecting
// to /login when unauthenticated. Compose onto any createServerFn that needs the
// caller's identity (e.g. hybrid direct-MySQL read loaders in Stage 3).
//
// Route-level gating (beforeLoad) uses getServerSession + requireSession/
// requireRole from ./session; this middleware is the server-function equivalent
// for RPC-style data reads.

import { createMiddleware } from "@tanstack/react-start";
import { getRequest } from "@tanstack/react-start/server";
import { redirect } from "@tanstack/react-router";

export interface AuthedContext {
  user: { id: string; email: string; role: string };
  activeOrg: { id: string; role: string } | null;
}

/** Requires a valid session; throws redirect to /login otherwise. */
export const authMiddleware = createMiddleware({ type: "function" }).server(
  async ({ next }) => {
    const { auth } = await import("./server");
    const request = getRequest();
    const session = await auth.api.getSession({ headers: request.headers });
    if (!session?.user) {
      throw redirect({ to: "/login" });
    }

    const activeOrganizationId =
      (session.session as { activeOrganizationId?: string | null })
        .activeOrganizationId ?? null;

    const ctx: AuthedContext = {
      user: {
        id: session.user.id,
        email: session.user.email ?? "",
        role: (session.user as { role?: string }).role ?? "user",
      },
      activeOrg: activeOrganizationId
        ? { id: activeOrganizationId, role: "member" }
        : null,
    };

    return next({ context: ctx });
  },
);
