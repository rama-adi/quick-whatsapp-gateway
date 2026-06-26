// Authenticated layout route. A TanStack Start pathless layout route (the
// leading "_" keeps it out of the URL) nesting every authed surface under one
// shell + one event-stream mount.
//
// STAGE 2: the session is resolved SERVER-SIDE in beforeLoad via the better-auth
// session (getServerSession), then gated with requireSession() — replacing the
// v1 client-side guard and the Stage-1 stub. The resolved AppSession rides the
// route context so the shell + nested loaders read it without re-fetching.

import { createFileRoute } from "@tanstack/react-router";
import { AppShell } from "~/components/shell/AppShell";
import {
  getServerSession,
  requireSession,
  type AppSession,
} from "~/lib/auth/session";

export const Route = createFileRoute("/_app")({
  beforeLoad: async (): Promise<{ session: AppSession }> => {
    const session = await getServerSession();
    return { session: requireSession(session) };
  },
  component: AppLayout,
});

function AppLayout() {
  const { session } = Route.useRouteContext();
  return <AppShell session={session} />;
}
