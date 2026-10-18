// User: session DETAIL layout — the shell around a single session's surfaces.
// §12 user surface. Renders a back link + the session name in the top bar
// (plus a segmented Overview / Chats / Contacts control below `md`; the sidebar
// carries that nav on desktop) and an <Outlet/> for the active sub-surface.
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
import {
  PageHeader,
  HeaderBack,
  HeaderTitle,
} from "~/components/shell/page-chrome";
import { SESSION_NAV, type SessionNavItem } from "~/components/shell/nav";
import { useSession } from "~/lib/api/hooks/sessions";
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
  // Same query key as the overview + sidebar, so this is a cache read.
  const session = useSession(sessionId);
  const name = session.data?.label || sessionId;

  return (
    <>
      {/* This layout owns the top-bar chrome for the whole session-detail
          surface (overview / chats / contacts), so nested routes don't render
          their own <PageHeader>. Keeping the layout body height-transparent
          (just the active child) is what lets the chat surface opt into fill
          mode below it.

          Sub-navigation: on desktop the sidebar shows this session's section
          (Overview / Chats / Contacts, see AppSidebar), so the header carries
          only the back link + the session name. Below `md` the sidebar is a
          sheet, so the same SESSION_NAV renders here as a segmented control. */}
      <PageHeader>
        <HeaderBack to="/user/sessions" label="All sessions" />
        <Separator
          orientation="vertical"
          className="data-[orientation=vertical]:h-4"
        />
        <HeaderTitle className="hidden md:block">{name}</HeaderTitle>
        <nav
          aria-label="Session sections"
          className="flex h-8 min-w-0 items-center gap-0.5 overflow-x-auto rounded-lg bg-muted p-[3px] md:hidden"
        >
          {SESSION_NAV.map((item) => (
            <SessionTab key={item.to} item={item} sessionId={sessionId} />
          ))}
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
  item,
  sessionId,
}: {
  item: SessionNavItem;
  sessionId: string;
}) {
  return (
    <Link
      to={item.to}
      params={{ sessionId }}
      activeOptions={{ exact: item.exact }}
      className="inline-flex h-full shrink-0 items-center rounded-md px-2.5 text-sm font-medium text-muted-foreground transition-[color,background-color,box-shadow] duration-200 ease-soft hover:text-foreground data-[status=active]:bg-background data-[status=active]:text-foreground data-[status=active]:shadow-xs dark:data-[status=active]:bg-input/40"
    >
      {item.label}
    </Link>
  );
}
