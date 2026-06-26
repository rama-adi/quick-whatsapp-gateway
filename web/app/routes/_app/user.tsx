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
// The org switcher lives here (top of every user surface) because sessions /
// keys / webhooks are all scoped to the active organization.

import { createFileRoute, Outlet, redirect } from "@tanstack/react-router";
import { OrgSwitcher } from "./user/-components/org-switcher";

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
  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between gap-3">
        <h2 className="text-sm font-medium text-muted-foreground">Workspace</h2>
        <OrgSwitcher />
      </div>
      <Outlet />
    </div>
  );
}
