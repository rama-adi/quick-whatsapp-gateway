// User: OAuth-apps LAYOUT. §12 user surface, scoped to the active org.
// oauth.md §6.2 (Sign in with WhatsApp — provider management).
//
// Owns the /user/oauth-apps path segment and renders an <Outlet/> for the list
// (oauth-apps.index) and the per-app detail (oauth-apps.$appId). Auth + org come
// from the parent _app/user beforeLoad gate; no re-fetch here.
//
// Opts the subtree into the shared event stream so the session picker + list
// reflect live session status (a bound session going down disables its apps).

import { createFileRoute, Outlet } from "@tanstack/react-router";
import { useEventStreamSubscription } from "~/lib/events/useEventStream";

export const Route = createFileRoute("/_app/user/oauth-apps")({
  component: OAuthAppsLayout,
});

function OAuthAppsLayout() {
  useEventStreamSubscription();
  return <Outlet />;
}
