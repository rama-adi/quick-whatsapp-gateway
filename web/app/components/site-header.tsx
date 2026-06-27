// App top bar. Tailored from the dashboard-01 `site-header` block: the demo
// "Documents" title was dropped. It carries the SidebarTrigger (which is also
// the mobile-nav affordance — the trigger opens the sidebar as a Sheet below
// `md`), a per-page chrome slot (back button / title / tabs / actions, filled
// by the active route via page-chrome), the impersonation badge, and the
// active-org switcher.
//
// The org switcher lives here (top-level chrome) for user-panel sessions, since
// every user surface (sessions/keys/webhooks) is scoped to the active org.
// Admin-only accounts work cross-org, so the switcher is hidden for them.

import { Separator } from "~/components/ui/separator";
import { SidebarTrigger } from "~/components/ui/sidebar";
import { PageHeaderSlot } from "./shell/page-chrome";
import { OrgSwitcher } from "./shell/org-switcher";
import type { AppSession } from "~/lib/auth/session";

export function SiteHeader({ session }: { session: AppSession }) {
  const showOrgSwitcher =
    session.userPanelEnabled && session.user.roles.includes("user");
  return (
    <header className="flex h-(--header-height) shrink-0 items-center gap-2 border-b transition-[width,height] ease-linear group-has-data-[collapsible=icon]/sidebar-wrapper:h-(--header-height)">
      <div className="flex h-full w-full items-center gap-2 px-4 lg:px-6">
        <SidebarTrigger className="-ml-1 shrink-0" />
        <Separator
          orientation="vertical"
          className="mx-1 data-[orientation=vertical]:h-4"
        />
        {/* Per-page chrome (back button, title, tabs, actions) is portaled here
            by the active route via <PageHeader>. flex-1 so it owns the middle
            and pushes the impersonation badge + org switcher to the right. */}
        <PageHeaderSlot className="flex min-w-0 flex-1 items-center gap-2" />
        {session.impersonating && (
          <span className="shrink-0 rounded bg-destructive px-2 py-0.5 text-xs font-medium text-destructive-foreground">
            Impersonating
          </span>
        )}
        {showOrgSwitcher && <OrgSwitcher />}
      </div>
    </header>
  );
}
