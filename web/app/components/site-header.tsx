// App top bar. Tailored from the dashboard-01 `site-header` block: the demo
// "Documents" title was dropped; it now carries the SidebarTrigger (which is
// also the mobile-nav affordance — the trigger opens the sidebar as a Sheet
// below `md`), the impersonation badge, and the live ConnectionPill.

import { Separator } from "~/components/ui/separator";
import { SidebarTrigger } from "~/components/ui/sidebar";
import { ConnectionPill } from "./shell/ConnectionPill";
import type { AppSession } from "~/lib/auth/session";

export function SiteHeader({ session }: { session: AppSession }) {
  return (
    <header className="flex h-(--header-height) shrink-0 items-center gap-2 border-b transition-[width,height] ease-linear group-has-data-[collapsible=icon]/sidebar-wrapper:h-(--header-height)">
      <div className="flex w-full items-center gap-2 px-4 lg:px-6">
        <SidebarTrigger className="-ml-1" />
        <Separator
          orientation="vertical"
          className="mx-1 data-[orientation=vertical]:h-4"
        />
        {session.impersonating && (
          <span className="rounded bg-destructive px-2 py-0.5 text-xs font-medium text-destructive-foreground">
            Impersonating
          </span>
        )}
        <div className="ml-auto flex items-center gap-3">
          <ConnectionPill />
        </div>
      </div>
    </header>
  );
}
