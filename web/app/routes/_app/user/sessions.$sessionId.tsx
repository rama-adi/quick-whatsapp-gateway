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

import { createFileRoute, Link, Outlet, useRouterState } from "@tanstack/react-router";
import { Separator } from "~/components/ui/separator";
import { cn } from "~/lib/utils";
import { PageHeader, HeaderBack } from "~/components/shell/page-chrome";
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

  return (
    <>
      {/* This layout owns the top-bar chrome for the whole session-detail
          surface (overview / chats / contacts), so nested routes don't render
          their own <PageHeader>. Keeping the layout body height-transparent
          (just the active child) is what lets the chat surface opt into fill
          mode below it. */}
      <PageHeader>
        <HeaderBack to="/user/sessions" label="All sessions" />
        <Separator
          orientation="vertical"
          className="data-[orientation=vertical]:h-4"
        />
        <nav className="flex min-w-0 items-center gap-1 overflow-x-auto">
          <SessionTab to="/user/sessions/$sessionId" sessionId={sessionId} exact>
            Overview
          </SessionTab>
          <SessionTab to="/user/sessions/$sessionId/chats" sessionId={sessionId}>
            Chats
          </SessionTab>
          <SessionTab
            to="/user/sessions/$sessionId/contacts"
            sessionId={sessionId}
          >
            Contacts
          </SessionTab>
        </nav>
      </PageHeader>

      {isBareSessionRoute ? (
        <SessionOverview sessionId={sessionId} />
      ) : (
        <Outlet />
      )}
    </>
  );
}

function SessionTab({
  to,
  sessionId,
  exact = false,
  children,
}: {
  to: string;
  sessionId: string;
  exact?: boolean;
  children: React.ReactNode;
}) {
  const base =
    "shrink-0 rounded-md px-2.5 py-1 text-sm text-muted-foreground hover:bg-accent hover:text-foreground";
  const active = "bg-accent font-medium text-foreground";
  return (
    <Link
      to={to as never}
      params={{ sessionId } as never}
      activeOptions={{ exact }}
      className={base}
      activeProps={{ className: cn(base, active) }}
    >
      {children}
    </Link>
  );
}
