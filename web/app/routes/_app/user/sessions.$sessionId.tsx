// User: session DETAIL layout — the shell around a single session's surfaces.
// §12 user surface. Renders a back link + the session's title and status in the
// top bar, the Overview / Chats / Contacts tabs in a row directly under it (on
// every breakpoint; the sidebar never changes shape), and an <Outlet/> for the
// active sub-surface.
//
// Ported from v1 user/session.detail.tsx (tag mvp-v1) — the detail shell with
// sub-navigation. Reshape notes:
//   - /user/sessions/:id (shell + nested) -> TanStack file-based LAYOUT route
//     /user/sessions/$sessionId rendering the header, tabs + <Outlet/>.
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
  PageSubnav,
  HeaderBack,
  HeaderTitle,
} from "~/components/shell/page-chrome";
import { Skeleton } from "~/components/ui/skeleton";
import { SESSION_NAV, type SessionNavItem } from "~/components/shell/nav";
import { useSession } from "~/lib/api/hooks/sessions";
import type { WASession } from "~/lib/api/types";
import { SessionStatusBadge } from "~/components/status-badge";
import { Button } from "~/components/ui/button";
import { titleHead } from "~/lib/head";
import { PlayIcon, RefreshCwIcon } from "lucide-react";
import { SessionOverview } from "./-components/session-overview";
import { sessionName, useSessionActions } from "./-components/user-ui";
import { UNTITLED_SESSION, sessionTitle } from "~/lib/format";

export const Route = createFileRoute("/_app/user/sessions/$sessionId")({
  head: () => titleHead("Session"),
  component: SessionDetailLayout,
});

function SessionDetailLayout() {
  const { sessionId } = Route.useParams();
  const pathname = useRouterState({
    select: (state) => state.location.pathname,
  });
  const isBareSessionRoute =
    pathname === `/user/sessions/${encodeURIComponent(sessionId)}`;
  // Same query key as the overview, so this is a cache read.
  const session = useSession(sessionId);

  return (
    <>
      {/* This layout owns the top chrome for the whole session-detail surface
          (overview / chats / contacts), so nested routes don't render their own
          <PageHeader>. Keeping the layout body height-transparent (just the
          active child) is what lets chats / contacts opt into fill mode below
          it.

          Header row: back link, the session's title and its status. Tab row
          (PageSubnav, under the header on every breakpoint): Overview / Chats /
          Contacts. Both stay mounted while the child routes swap, so nothing
          shifts when moving between a session's sections. */}
      <PageHeader>
        <HeaderBack to="/user/sessions" label="All sessions" />
        <Separator
          orientation="vertical"
          className="data-[orientation=vertical]:h-4"
        />
        {session.data ? (
          <HeaderTitle>
            <span title={sessionTitle(session.data)}>
              {sessionTitle(session.data)}
            </span>
          </HeaderTitle>
        ) : session.isPending ? (
          <Skeleton className="h-4 w-28" />
        ) : (
          <HeaderTitle>Session</HeaderTitle>
        )}
        {session.data && (
          <SessionStatusBadge status={session.data.status} className="shrink-0" />
        )}
      </PageHeader>
      <PageSubnav>
        <nav
          aria-label="Session sections"
          className="-mb-px flex items-center gap-1 px-1.5 lg:px-2.5"
        >
          {SESSION_NAV.map((item) => (
            <SessionTab key={item.to} item={item} sessionId={sessionId} />
          ))}
        </nav>
      </PageSubnav>

      {isBareSessionRoute ? (
        <SessionOverview sessionId={sessionId} />
      ) : session.data && session.data.status !== "working" ? (
        // Chats / contacts are fill-mode surfaces: keep the banner + child in a
        // height-clamped column so the child still fills the rest.
        <div className="flex h-full min-h-0 flex-col">
          <NotConnectedBanner session={session.data} />
          <div className="min-h-0 flex-1">
            <Outlet />
          </div>
        </div>
      ) : (
        <Outlet />
      )}
    </>
  );
}

/**
 * On Chats / Contacts, explain why the view may be empty or stale when the
 * session isn't connected, with the one action that fixes it.
 */
function NotConnectedBanner({ session }: { session: WASession }) {
  const actions = useSessionActions();
  const pending = Boolean(actions.pendingFor(session.id));
  const paired = Boolean(session.waJid);
  // "Untitled session isn't linked…" reads oddly; fall back to "This session".
  const title = sessionName(session);
  const name = title === UNTITLED_SESSION ? "This session" : title;

  let text: string;
  let action: "start" | "restart" | "link" | null = null;
  if (!paired) {
    text = `${name} isn't linked to a phone yet, so there's nothing to show.`;
    action = "link";
  } else if (session.status === "stopped") {
    text = `${name} is stopped. Start it to receive new messages.`;
    action = "start";
  } else if (session.status === "failed") {
    text = `${name} lost its connection. Restart it to receive new messages.`;
    action = "restart";
  } else if (session.status === "logged_out") {
    text = `${name} was unlinked from the phone. Link it again to receive new messages.`;
    action = "link";
  } else {
    text = `${name} is reconnecting. New messages appear once it's connected.`;
  }

  return (
    <div
      role="status"
      className="flex shrink-0 flex-wrap items-center justify-between gap-x-4 gap-y-2 border-b border-border/60 bg-muted/40 px-4 py-2 text-sm text-muted-foreground"
    >
      <span>{text}</span>
      {action === "link" ? (
        <Button asChild size="xs" variant="outline">
          <Link to="/user/sessions/$sessionId" params={{ sessionId: session.id }}>
            Link phone
          </Link>
        </Button>
      ) : action ? (
        <Button
          size="xs"
          variant="outline"
          disabled={pending}
          onClick={() =>
            void actions.run(session, action === "restart" ? "restart" : "start")
          }
        >
          {action === "start" ? (
            <PlayIcon aria-hidden data-icon="inline-start" />
          ) : (
            <RefreshCwIcon aria-hidden data-icon="inline-start" />
          )}
          {action === "start" ? "Start" : "Restart"}
        </Button>
      ) : null}
    </div>
  );
}

function SessionTab({
  item,
  sessionId,
}: {
  item: SessionNavItem;
  sessionId: string;
}) {
  const Icon = item.icon;
  // Underline tab: the active one gets a 2px bar that sits on the top chrome's
  // hairline (the nav's -mb-px). Active state is route-driven: <Link> sets
  // data-status="active" and aria-current="page".
  return (
    <Link
      to={item.to}
      params={{ sessionId }}
      activeOptions={{ exact: item.exact }}
      className="group/tab relative inline-flex h-10 shrink-0 items-center px-1 text-sm font-medium text-muted-foreground outline-none transition-colors duration-150 ease-soft hover:text-foreground data-[status=active]:text-foreground after:absolute after:inset-x-1 after:bottom-0 after:h-0.5 after:rounded-full after:bg-foreground after:opacity-0 after:transition-opacity after:duration-200 after:ease-soft data-[status=active]:after:opacity-100"
    >
      <span className="inline-flex h-7 items-center gap-1.5 rounded-md px-2 transition-colors duration-150 ease-soft group-hover/tab:bg-muted group-focus-visible/tab:ring-3 group-focus-visible/tab:ring-ring/40">
        <Icon className="size-4 opacity-70 group-data-[status=active]/tab:opacity-100" aria-hidden />
        {item.label}
      </span>
    </Link>
  );
}
