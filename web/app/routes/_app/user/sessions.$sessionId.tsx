// User: session DETAIL layout — the shell around a single session's surfaces.
// §12 user surface. Renders a back link + a tab strip (Overview / Chats /
// Contacts) and an <Outlet/> for the active sub-surface.
//
// Ported from v1 user/session.detail.tsx (tag mvp-v1) — the detail shell with
// sub-navigation. Reshape notes:
//   - /user/sessions/:id (shell + nested) -> TanStack file-based LAYOUT route
//     /user/sessions/$sessionId rendering the tabs + <Outlet/>.
//   - The OVERVIEW (status/QR/pairing) lives in the index child
//     (sessions.$sessionId.index.tsx). CHATS and CONTACTS are sibling-owned
//     surfaces (viewer/contacts) that mount as the other children
//     (sessions.$sessionId.chats / .contacts) — this layout's <Outlet/> is what
//     lets them render, and these tabs link to them.
//   - react-router NavLink -> @tanstack/react-router <Link> with activeProps and
//     activeOptions ({exact} for the index tab).

import {
  createFileRoute,
  Link,
  Outlet,
  useRouterState,
} from "@tanstack/react-router";
import { ArrowLeftIcon } from "lucide-react";
import { Button } from "~/components/ui/button";
import { cn } from "~/lib/utils";
import { SessionOverview } from "./-components/session-overview";

export const Route = createFileRoute("/_app/user/sessions/$sessionId")({
  component: SessionDetailLayout,
});

function SessionDetailLayout() {
  const { sessionId } = Route.useParams();
  const pathname = useRouterState({
    select: (state) => state.location.pathname,
  });
  const isBareSessionRoute =
    pathname === `/user/sessions/${encodeURIComponent(sessionId)}`;

  const tabBase =
    "px-3 py-2 text-sm text-muted-foreground hover:text-foreground";
  const tabActive = "border-b-2 border-primary font-medium text-foreground";

  return (
    <div className="space-y-4">
      <Button asChild variant="ghost" size="sm" className="gap-1.5">
        <Link to="/user/sessions">
          <ArrowLeftIcon className="size-4" aria-hidden />
          All sessions
        </Link>
      </Button>

      <div className="flex items-center gap-1 border-b">
        <Link
          to="/user/sessions/$sessionId"
          params={{ sessionId }}
          activeOptions={{ exact: true }}
          className={tabBase}
          activeProps={{ className: cn(tabBase, tabActive) }}
        >
          Overview
        </Link>
        <Link
          to="/user/sessions/$sessionId/chats"
          params={{ sessionId }}
          className={tabBase}
          activeProps={{ className: cn(tabBase, tabActive) }}
        >
          Chats
        </Link>
        <Link
          to="/user/sessions/$sessionId/contacts"
          params={{ sessionId }}
          className={tabBase}
          activeProps={{ className: cn(tabBase, tabActive) }}
        >
          Contacts
        </Link>
      </div>

      {isBareSessionRoute ? (
        <SessionOverview sessionId={sessionId} />
      ) : (
        <Outlet />
      )}
    </div>
  );
}
