// App top bar. Tailored from the dashboard-01 `site-header` block: the demo
// "Documents" title was dropped. It carries the SidebarTrigger (which is also
// the mobile-nav affordance — the trigger opens the sidebar as a Sheet below
// `md`), a per-page chrome slot (back button / title / tabs / actions, filled
// by the active route via page-chrome), an optional section-tab row under it
// (PageSubnavSlot, e.g. one session's Overview / Chats / Contacts), the "Viewing as … / Stop impersonating"
// control while an admin impersonates a user, and the active-org switcher.
//
// The org switcher lives here (top-level chrome) for user-panel sessions, since
// every user surface (sessions/keys/webhooks) is scoped to the active org.
// Admin-only accounts work cross-org, so the switcher is hidden for them.

import { useMatch } from "@tanstack/react-router";
import { Separator } from "~/components/ui/separator";
import { SidebarTrigger } from "~/components/ui/sidebar";
import { PageHeaderSlot, PageSubnavSlot } from "./shell/page-chrome";
import { OrgSwitcher } from "./shell/org-switcher";
import { ImpersonationButton } from "./shell/impersonation-button";
import type { AppSession } from "~/lib/auth/session";

export function SiteHeader({ session }: { session: AppSession }) {
  const showOrgSwitcher =
    session.userPanelEnabled && session.user.roles.includes("user");
  // Inside one session the header row carries the back link, the session's
  // title and its status, so on mobile the org switcher shrinks to its avatar
  // there to leave the title room.
  const inSession = Boolean(
    useMatch({ from: "/_app/user/sessions/$sessionId", shouldThrow: false }),
  );
  return (
    // One hairline under the whole top chrome: the header row plus, when a
    // layout provides one, its section tabs (whose active underline sits on
    // this border).
    <div className="shrink-0 border-b border-border/60">
      <header className="flex h-(--header-height) items-center">
        <div className="flex h-full w-full min-w-0 items-center gap-2 px-3 lg:px-4">
          <SidebarTrigger className="-ml-0.5 shrink-0 text-muted-foreground hover:text-foreground" />
          <Separator
            orientation="vertical"
            className="mr-1 data-[orientation=vertical]:h-4"
          />
          {/* Per-page chrome (back button, title, tabs, actions) is portaled here
              by the active route via <PageHeader>. flex-1 so it owns the middle
              and pushes the impersonation control + org switcher to the right. */}
          <PageHeaderSlot className="flex min-w-0 flex-1 items-center gap-2" />
          {session.impersonating && <ImpersonationButton session={session} />}
          {showOrgSwitcher && <OrgSwitcher compactOnMobile={inSession} />}
        </div>
      </header>
      <PageSubnavSlot />
    </div>
  );
}
