// Authenticated app shell: the role-aware sidebar + outlet. The session is
// resolved by the layout route (_app.tsx) and passed in as a prop; this
// component is purely presentational.
// Owned by the foundation agent.
//
// ConfirmProvider hosts the single promise-based confirm dialog (useConfirm()).
//
// The EventStreamProvider is mounted here so the whole authed tree can read the
// connection status, but it stays IDLE until a surface opts in with
// useEventStreamSubscription() — the socket is page-scoped, not global.
//
// Layout: the window is the "frame" (bg-sidebar). The sidebar sits directly on
// it (inset variant, no border) and the header + page live in one rounded,
// hairline-bordered panel (SidebarInset). Below `md` the sidebar becomes a
// sheet and the panel keeps an 8px frame gutter on all sides.
//
// Height chain: the shell is clamped to the viewport (h-svh) and the content
// region is the only scroller, so the sidebar + top bar stay fixed and the body
// never scrolls. Pages opt into "fill" mode (no padding, page owns its internal
// scrolling) via page-chrome — see AppMain below.

import { Outlet } from "@tanstack/react-router";
import type { AppSession } from "~/lib/auth/session";
import { SessionProvider } from "~/lib/auth/context";
import { EventStreamProvider } from "~/lib/events/EventStreamProvider";
import { PageChromeProvider, usePageFill } from "~/components/shell/page-chrome";
import { useRouteTransition } from "~/components/shell/route-transition";
import { SidebarInset, SidebarProvider } from "~/components/ui/sidebar";
import { AppSidebar } from "~/components/app-sidebar";
import { SiteHeader } from "~/components/site-header";
import { ConfirmProvider } from "~/components/confirm-dialog";
import { cn } from "~/lib/utils";

export function AppShell({ session }: { session: AppSession }) {
  return (
    <SessionProvider session={session}>
      <EventStreamProvider>
        <PageChromeProvider>
          <ConfirmProvider>
            <SidebarProvider
              className="h-svh overflow-hidden bg-sidebar"
              style={
                {
                  "--sidebar-width": "16rem",
                  "--header-height": "3rem",
                } as React.CSSProperties
              }
            >
              <AppSidebar session={session} variant="inset" />
              <SidebarInset className="min-h-0 min-w-0 overflow-hidden max-md:m-2 max-md:rounded-panel max-md:border max-md:border-sidebar-border/80 max-md:shadow-panel">
                <SiteHeader session={session} />
                <AppMain />
              </SidebarInset>
            </SidebarProvider>
          </ConfirmProvider>
        </PageChromeProvider>
      </EventStreamProvider>
    </SessionProvider>
  );
}

// The scrollable content region. Default surfaces get comfortable padding,
// a centered max-w-screen-2xl column, and scroll internally; "fill" surfaces
// (e.g. chat) get a height-clamped, padding-free box and manage their own
// scroll areas inside. The inner wrapper eases in on navigation.
function AppMain() {
  const fill = usePageFill();
  const transitionRef = useRouteTransition<HTMLDivElement>();
  return (
    <div
      data-slot="app-main"
      className={cn(
        "min-h-0 min-w-0 flex-1",
        fill ? "overflow-hidden" : "overflow-auto p-4 sm:p-5 xl:p-6",
      )}
    >
      <div
        ref={transitionRef}
        className={cn("w-full", fill ? "h-full" : "mx-auto max-w-screen-2xl")}
      >
        <Outlet />
      </div>
    </div>
  );
}
