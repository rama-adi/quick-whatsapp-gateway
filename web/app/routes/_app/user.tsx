// User-panel layout route (§12 "User" surface). Nests /user/sessions,
// /user/keys, /user/webhooks under one org-aware shell.
//
// Porting note: replaces the v1 user/_guard.ts + user/_ui.tsx layout (tag
// mvp-v1). The v1 client-side `requireUserPanel`
// clientLoader becomes a server-resolved `beforeLoad` gate: the parent _app
// route already resolved the better-auth session into AppSession (server-side,
// §12) and put it on the route context, so we gate from context with zero
// re-fetch. Unauthorized users are redirected home (the nav hiding in the
// FROZEN shell is cosmetic; this is the real gate).
//
// The active-org switcher used to live here; it now sits in the app top bar
// (components/site-header.tsx) as global chrome for user-panel sessions, since
// sessions / keys / webhooks are all scoped to the active organization.

import { createFileRoute, Outlet, redirect } from "@tanstack/react-router";

export const Route = createFileRoute("/_app/user")({
  beforeLoad: ({ context }) => {
    const { session } = context;
    if (!session.userPanelEnabled || !session.user.roles.includes("user")) {
      throw redirect({ to: "/" });
    }
  },
  component: UserLayout,
});

function UserLayout() {
  return <Outlet />;
}
