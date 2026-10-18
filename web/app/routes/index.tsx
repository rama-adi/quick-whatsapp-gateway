// Root entry point: the role-routed landing (§12).
//
// Resolved SERVER-SIDE in beforeLoad: read the better-auth session via
// getServerSession and bounce to the landing route (landingPath):
//   - unauthenticated              -> /login
//   - user panel (incl. admins)    -> /user/home
//   - admin without the user panel -> /admin/sessions
// The redirect target is a fully-built runtime string, so we use the
// `redirect({ href })` escape hatch (the same pattern the auth surface uses)
// rather than a typed `to`, keeping "/" independent of the sibling surfaces'
// route types.

import { createFileRoute, redirect } from "@tanstack/react-router";
import { getServerSession, landingPath } from "~/lib/auth/session";

export const Route = createFileRoute("/")({
  beforeLoad: async () => {
    const session = await getServerSession();
    if (!session) {
      throw redirect({ href: "/login" });
    }
    const landing = landingPath(session);
    if (landing) {
      throw redirect({ href: landing });
    }
    // Authenticated but no usable surface (e.g. user panel disabled and not an
    // admin) — fall through to the minimal shell below.
  },
  component: Home,
});

function Home() {
  return (
    <main className="container mx-auto flex min-h-screen flex-col items-center justify-center gap-4 p-8 text-center">
      <h1 className="text-3xl font-semibold">WA Gateway</h1>
      <p className="text-muted-foreground">
        You are signed in, but no workspace is available for your account.
        Contact an administrator.
      </p>
    </main>
  );
}
