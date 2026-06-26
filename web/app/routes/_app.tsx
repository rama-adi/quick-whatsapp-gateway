// Authenticated layout route. Ports the v1 AppShell layout-route to a TanStack
// Start pathless layout route (the leading "_" keeps it out of the URL while
// nesting every authed surface under one shell + one event-stream mount).
//
// STAGE 1 (skeleton): the session is STUBBED so the shell renders and the build
// typechecks without a live backend. STAGE 2 replaces `loadSession()` below with
// the real better-auth session read (server-side) + JWT mint, and re-enables
// requireSession()/role gating. The reusable resolver lives in
// ~/lib/auth/session.ts (loadSession / requireSession / requireRole) — keep it.

import { createFileRoute } from "@tanstack/react-router";
import { AppShell } from "~/components/shell/AppShell";
import type { AppSession } from "~/lib/auth/session";

// TODO(stage-2): replace with the real better-auth session resolution.
// e.g. beforeLoad: read the better-auth session server-side, mint the JWT,
// then requireSession(session). For now we hand the shell a stub so the
// skeleton renders.
const STUB_SESSION: AppSession = {
  user: { id: "", email: "", roles: ["super_admin", "user"] },
  userPanelEnabled: true,
  impersonating: false,
};

export const Route = createFileRoute("/_app")({
  beforeLoad: (): { session: AppSession } => {
    return { session: STUB_SESSION };
  },
  component: AppLayout,
});

function AppLayout() {
  const { session } = Route.useRouteContext();
  return <AppShell session={session} />;
}
