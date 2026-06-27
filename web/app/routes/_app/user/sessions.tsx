// User: sessions LAYOUT. §12 user surface, scoped to the active org.
//
// This route owns the /user/sessions path segment and is the PARENT of both the
// list (sessions.index.tsx) and the per-session detail (sessions.$sessionId and
// its nested chats/contacts surfaces). Its only job is to render an <Outlet/> so
// those children mount; the list itself lives in the index child. Without this
// Outlet a child route (e.g. /user/sessions/$sessionId) has nowhere to render
// and the list bleeds through underneath it.
//
// Every session surface (list status, detail/overview, chats, contacts) needs
// live events, so this layout opts the whole subtree into the shared event
// stream once — children just read useEventStream()/usePollingInterval().

import { createFileRoute, Outlet } from "@tanstack/react-router";
import { useEventStreamSubscription } from "~/lib/events/useEventStream";

export const Route = createFileRoute("/_app/user/sessions")({
  component: SessionsLayout,
});

function SessionsLayout() {
  useEventStreamSubscription();
  return <Outlet />;
}
