// User: session OVERVIEW (the index of /user/sessions/$sessionId).

import { createFileRoute } from "@tanstack/react-router";
import { SessionOverview } from "./-components/session-overview";

export const Route = createFileRoute("/_app/user/sessions/$sessionId/")({
  component: SessionOverviewRoute,
});

function SessionOverviewRoute() {
  const { sessionId } = Route.useParams();
  return <SessionOverview sessionId={sessionId} />;
}
