// Authenticated app shell: renders the role-aware sidebar + outlet and gates
// the event stream. The session is resolved by the layout route (_app.tsx) and
// passed in as a prop; this component is purely presentational.
// FROZEN — owned by the foundation agent.
//
// Ported from react-router to TanStack Start:
//   - clientLoader + useLoaderData moved up into routes/_app.tsx
//   - NavLink -> @tanstack/react-router <Link> with activeProps/inactiveProps
//   - <Outlet/> from @tanstack/react-router

import { Link, Outlet } from "@tanstack/react-router";
import type { AppSession } from "~/lib/auth/session";
import { SessionProvider } from "~/lib/auth/context";
import { EventStreamProvider } from "~/lib/events/EventStreamProvider";
import { visibleNav, type NavItem } from "./nav";
import { ConnectionPill } from "./ConnectionPill";
import { UserMenu } from "./UserMenu";
import { cn } from "~/lib/utils";

export function AppShell({ session }: { session: AppSession }) {
  const items = visibleNav(session);
  const adminItems = items.filter((i) => i.group === "Admin");
  const workspaceItems = items.filter((i) => i.group === "Workspace");

  return (
    <SessionProvider session={session}>
      <EventStreamProvider enabled>
        <div className="flex min-h-screen bg-background text-foreground">
          <aside className="hidden w-64 shrink-0 flex-col border-r bg-sidebar text-sidebar-foreground md:flex">
            <div className="flex h-14 items-center gap-2 border-b px-4 font-semibold">
              WA Gateway
            </div>
            <nav className="flex-1 space-y-6 overflow-y-auto p-3">
              {adminItems.length > 0 && (
                <NavSection title="Admin" items={adminItems} />
              )}
              {workspaceItems.length > 0 && (
                <NavSection title="Workspace" items={workspaceItems} />
              )}
            </nav>
          </aside>

          <div className="flex min-w-0 flex-1 flex-col">
            <header className="flex h-14 items-center justify-between gap-3 border-b px-4">
              <div className="flex items-center gap-3">
                {session.impersonating && (
                  <span className="rounded bg-destructive px-2 py-0.5 text-xs font-medium text-destructive-foreground">
                    Impersonating
                  </span>
                )}
              </div>
              <div className="flex items-center gap-3">
                <ConnectionPill />
                <UserMenu session={session} />
              </div>
            </header>
            <main className="min-w-0 flex-1 overflow-auto p-4 md:p-6">
              <Outlet />
            </main>
          </div>
        </div>
      </EventStreamProvider>
    </SessionProvider>
  );
}

function NavSection({ title, items }: { title: string; items: NavItem[] }) {
  return (
    <div>
      <p className="mb-1 px-2 text-xs font-medium uppercase tracking-wide text-muted-foreground">
        {title}
      </p>
      <ul className="space-y-0.5">
        {items.map((item) => (
          <li key={item.to}>
            <Link
              to={item.to}
              className="block rounded-md px-2 py-1.5 text-sm transition-colors text-sidebar-foreground/80 hover:bg-sidebar-accent/60"
              activeProps={{
                className: cn(
                  "block rounded-md px-2 py-1.5 text-sm transition-colors",
                  "bg-sidebar-accent text-sidebar-accent-foreground font-medium",
                ),
              }}
            >
              {item.label}
            </Link>
          </li>
        ))}
      </ul>
    </div>
  );
}
