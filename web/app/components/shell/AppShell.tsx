// Authenticated app shell: resolves the session, gates the event stream, and
// renders the role-aware sidebar + outlet.
// FROZEN — owned by the foundation agent. This is a LAYOUT ROUTE (see routes.ts).

import { NavLink, Outlet, useLoaderData } from "react-router";
import type { Route } from "./+types/AppShell";
import { loadSession, requireSession, type AppSession } from "~/lib/auth/session";
import { SessionProvider } from "~/lib/auth/context";
import { EventStreamProvider } from "~/lib/events/EventStreamProvider";
import { visibleNav, type NavItem } from "./nav";
import { ConnectionPill } from "./ConnectionPill";
import { UserMenu } from "./UserMenu";
import { cn } from "~/lib/utils";

export async function clientLoader(): Promise<{ session: AppSession }> {
  const session = await loadSession();
  return { session: requireSession(session) };
}

export default function AppShell() {
  const { session } = useLoaderData() as { session: AppSession };
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
            <NavLink
              to={item.to}
              className={({ isActive }) =>
                cn(
                  "block rounded-md px-2 py-1.5 text-sm transition-colors",
                  isActive
                    ? "bg-sidebar-accent text-sidebar-accent-foreground font-medium"
                    : "text-sidebar-foreground/80 hover:bg-sidebar-accent/60",
                )
              }
            >
              {item.label}
            </NavLink>
          </li>
        ))}
      </ul>
    </div>
  );
}

export function ErrorBoundary({ error }: Route.ErrorBoundaryProps) {
  return (
    <div className="p-8">
      <h1 className="text-lg font-semibold">Something went wrong</h1>
      <p className="text-sm text-muted-foreground">
        {error instanceof Error ? error.message : "Unexpected error."}
      </p>
    </div>
  );
}
