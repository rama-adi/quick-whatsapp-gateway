// /admin layout route — super_admin gate for the whole admin surface.
//
// Ports the v1 admin/_guard.ts (requireAdmin clientLoader) to a TanStack Start
// beforeLoad. The parent _app route already resolved the AppSession server-side
// and put it on the route context ({ session }); we read it here (no second
// fetch) and gate by the platform role. requireRole throws redirect({to:"/"})
// when the role is missing — the same behavior as the v1 guard.
//
// This is a pathless layout for /admin/* children; it renders just <Outlet/>
// (the AppShell chrome comes from the _app parent).

import { Outlet, createFileRoute } from "@tanstack/react-router";
import { requireRole } from "~/lib/auth/session";

export const Route = createFileRoute("/_app/admin")({
  beforeLoad: ({ context }) => {
    // `context.session` is provided by the parent _app beforeLoad.
    requireRole(context.session, "super_admin");
  },
  component: () => <Outlet />,
});
