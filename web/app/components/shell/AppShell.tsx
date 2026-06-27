// Authenticated app shell: the role-aware sidebar + outlet, gating the event
// stream. The session is resolved by the layout route (_app.tsx) and passed in
// as a prop; this component is purely presentational.
// Owned by the foundation agent.
//
// Rebuilt on the shadcn `sidebar` primitive (from the dashboard-01 block),
// replacing the v1 hand-rolled <aside>. The SidebarProvider gives us the
// collapsible rail, the mobile Sheet drawer (the old shell had NO mobile nav),
// the Cmd/Ctrl+B toggle, and cookie-persisted collapsed state. AppSidebar holds
// the nav + account menu; SiteHeader carries the trigger + connection status.

import { Outlet } from "@tanstack/react-router";
import type { AppSession } from "~/lib/auth/session";
import { SessionProvider } from "~/lib/auth/context";
import { EventStreamProvider } from "~/lib/events/EventStreamProvider";
import { SidebarInset, SidebarProvider } from "~/components/ui/sidebar";
import { AppSidebar } from "~/components/app-sidebar";
import { SiteHeader } from "~/components/site-header";

export function AppShell({ session }: { session: AppSession }) {
  return (
    <SessionProvider session={session}>
      <EventStreamProvider enabled>
        <SidebarProvider
          style={
            {
              "--sidebar-width": "16rem",
              "--header-height": "3.5rem",
            } as React.CSSProperties
          }
        >
          <AppSidebar session={session} />
          <SidebarInset>
            <SiteHeader session={session} />
            <main className="min-w-0 flex-1 overflow-auto p-4 md:p-6">
              <Outlet />
            </main>
          </SidebarInset>
        </SidebarProvider>
      </EventStreamProvider>
    </SessionProvider>
  );
}
