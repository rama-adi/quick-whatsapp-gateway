// App session resolution + route guards (§12) — better-auth edition.
//
// Replaces the v1 Authula capability-probe. The session is resolved SERVER-SIDE
// from the better-auth cookie via auth.api.getSession(headers), exposed to
// routes through a server function (getServerSession) so route beforeLoad guards
// run on the server. Client-side route guards from v1 are gone (§12).
//
// AppSession keeps the v1 SHAPE the shell already consumes (user.roles[],
// userPanelEnabled, impersonating) so AppShell/nav/UserMenu need no changes:
//   - platform role super_admin -> roles ["super_admin","user"] (admins can also
//     use the user panel); everyone else -> ["user"].
//   - activeOrg + orgRole are added for the org-aware surfaces.
//   - userPanelEnabled comes from USER_REGISTRATION_ENABLED (matches the §12 gate).

import { redirect } from "@tanstack/react-router";
import { createServerFn } from "@tanstack/react-start";
import { getRequest } from "@tanstack/react-start/server";

export type AppRole = "super_admin" | "user";

export interface AppSession {
  user: {
    id: string;
    email: string;
    roles: AppRole[];
  };
  /** The member's active organization, if any. */
  activeOrg: { id: string; role: string } | null;
  userPanelEnabled: boolean;
  impersonating: boolean;
}

/**
 * Server function: resolve the current better-auth session into an AppSession,
 * or null if unauthenticated. Runs only on the server (reads the cookie via the
 * request headers). Safe to call from route beforeLoad/loaders.
 */
export const getServerSession = createServerFn({ method: "GET" }).handler(
  async (): Promise<AppSession | null> => {
    // Imported lazily inside the handler so the server-only auth module never
    // leaks into the client bundle.
    const { auth } = await import("./server");
    const request = getRequest();
    const session = await auth.api.getSession({ headers: request.headers });
    if (!session?.user) return null;

    const platformRole = (session.user as { role?: string }).role ?? "user";
    const roles: AppRole[] =
      platformRole === "super_admin" ? ["super_admin", "user"] : ["user"];

    const activeOrganizationId =
      (session.session as { activeOrganizationId?: string | null })
        .activeOrganizationId ?? null;

    let activeOrg: AppSession["activeOrg"] = null;
    if (activeOrganizationId) {
      // Resolve the member role for the active org for the org-aware surfaces.
      const role = await getActiveOrgRole(session.user.id, activeOrganizationId);
      activeOrg = { id: activeOrganizationId, role: role ?? "member" };
    }

    return {
      user: {
        id: session.user.id,
        email: session.user.email ?? "",
        roles,
      },
      activeOrg,
      userPanelEnabled: process.env.USER_REGISTRATION_ENABLED !== "false",
      impersonating: Boolean(
        (session.session as { impersonatedBy?: string | null }).impersonatedBy,
      ),
    };
  },
);

async function getActiveOrgRole(
  userId: string,
  organizationId: string,
): Promise<string | null> {
  const { db } = await import("~/lib/db");
  const { member } = await import("~/lib/db/auth-schema");
  const { and, eq } = await import("drizzle-orm");
  try {
    const rows = await db
      .select({ role: member.role })
      .from(member)
      .where(and(eq(member.userId, userId), eq(member.organizationId, organizationId)))
      .limit(1);
    return rows[0]?.role ?? null;
  } catch {
    return null;
  }
}

/** Guard for protected loaders: throws a redirect to /login when unauthenticated. */
export function requireSession(s: AppSession | null): AppSession {
  if (!s) {
    throw redirect({ to: "/login" });
  }
  return s;
}

/** Guard for role-gated loaders: throws a redirect home when the role is missing. */
export function requireRole(s: AppSession, role: AppRole): void {
  if (!s.user.roles.includes(role)) {
    throw redirect({ to: "/" });
  }
}

export function hasRole(s: AppSession, role: AppRole): boolean {
  return s.user.roles.includes(role);
}
