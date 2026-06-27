// Authenticated app shell: the role-aware sidebar + outlet. The session is
// resolved by the layout route (_app.tsx) and passed in as a prop; this
// component is purely presentational.
// Owned by the foundation agent.
//
// The EventStreamProvider is mounted here so the whole authed tree can read the
// connection status, but it stays IDLE until a surface opts in with
// useEventStreamSubscription() — the socket is page-scoped, not global.
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
import { SidebarInset, SidebarProvider } from "~/components/ui/sidebar";
import { AppSidebar } from "~/components/app-sidebar";
import { SiteHeader } from "~/components/site-header";
import { cn } from "~/lib/utils";

export function AppShell({ session }: { session: AppSession }) {
  return (
    <SessionProvider session={session}>
      <EventStreamProvider>
        <PageChromeProvider>
          <SidebarProvider
            className="h-svh overflow-hidden"
            style={
              {
                "--sidebar-width": "16rem",
                "--header-height": "3.5rem",
              } as React.CSSProperties
            }
          >
            <AppSidebar session={session} />
            <SidebarInset className="min-h-0 overflow-hidden">
              <SiteHeader session={session} />
              <AppMain />
            </SidebarInset>
          </SidebarProvider>
        </PageChromeProvider>
      </EventStreamProvider>
    </SessionProvider>
  );
}

// The scrollable content region. Default surfaces get comfortable padding and
// scroll internally; "fill" surfaces (e.g. chat) get a height-clamped,
// padding-free box and manage their own scroll areas inside.
function AppMain() {
  const fill = usePageFill();
  return (
    <div
      className={cn(
        "min-h-0 min-w-0 flex-1",
        fill ? "overflow-hidden" : "overflow-auto p-4 md:p-6",
      )}
    >
      <Outlet />
    </div>
  );
}
