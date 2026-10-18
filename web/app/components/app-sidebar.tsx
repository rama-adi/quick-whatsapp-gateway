// The application sidebar, built on the shadcn `sidebar` primitive (collapsing
// to icons, the mobile Sheet drawer, Cmd/Ctrl+B, and tooltips come for free).
//
// It sits directly on the shell frame (AppShell renders it with
// variant="inset"): brand row on top, the role-filtered nav model
// (shell/nav.ts) in the middle, and the docs link + account chip at the
// bottom. Inside /user/sessions/$sessionId/* it also shows a contextual
// section for that session (Overview / Chats / Contacts).

import { Link, useMatch } from "@tanstack/react-router";
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
} from "~/components/ui/sidebar";
import { MessageSquareText } from "lucide-react";
import { useSession } from "~/lib/api/hooks/sessions";
import {
  visibleNav,
  DOCS_NAV,
  SESSION_NAV,
  type NavItem,
} from "./shell/nav";
import { NavUser } from "./nav-user";
import type { AppSession } from "~/lib/auth/session";

export function AppSidebar({
  session,
  ...props
}: { session: AppSession } & React.ComponentProps<typeof Sidebar>) {
  const items = visibleNav(session);
  const adminItems = items.filter((i) => i.group === "Admin");
  const workspaceItems = items.filter((i) => i.group === "Workspace");
  const DocsIcon = DOCS_NAV.icon;
  // Non-throwing lookup: the session-detail layout is only in the match tree
  // while the user is inside one session's surfaces.
  const sessionMatch = useMatch({
    from: "/_app/user/sessions/$sessionId",
    shouldThrow: false,
  });
  const sessionId = sessionMatch?.params.sessionId;

  return (
    <Sidebar collapsible="icon" {...props}>
      <SidebarHeader>
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton
              asChild
              size="lg"
              tooltip="WA Gateway"
              className="h-10 gap-2.5 px-1.5 text-sidebar-foreground hover:bg-sidebar-accent/50 group-data-[collapsible=icon]:p-0.5!"
            >
              <Link to="/">
                <span className="flex size-7 shrink-0 items-center justify-center rounded-lg bg-primary text-primary-foreground shadow-xs">
                  <MessageSquareText className="size-4!" aria-hidden />
                </span>
                <span className="truncate text-sm font-semibold tracking-tight">
                  WA Gateway
                </span>
              </Link>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarHeader>

      <SidebarContent>
        {sessionId && <SessionNavGroup sessionId={sessionId} />}
        {workspaceItems.length > 0 && (
          <NavGroup label="Workspace" items={workspaceItems} />
        )}
        {adminItems.length > 0 && <NavGroup label="Admin" items={adminItems} />}
      </SidebarContent>

      <SidebarFooter className="gap-1">
        <SidebarMenu>
          <SidebarMenuItem>
            {/* Plain anchor: /docs is a separate surface (its own fumadocs
                layout/provider), so a full navigation is intentional. */}
            <SidebarMenuButton asChild tooltip={DOCS_NAV.label}>
              <a href={DOCS_NAV.href}>
                <DocsIcon aria-hidden />
                <span>{DOCS_NAV.label}</span>
              </a>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
        <NavUser session={session} />
      </SidebarFooter>
    </Sidebar>
  );
}

function NavGroup({ label, items }: { label: string; items: NavItem[] }) {
  return (
    <SidebarGroup>
      <SidebarGroupLabel>{label}</SidebarGroupLabel>
      <SidebarMenu>
        {items.map((item) => {
          const Icon = item.icon;
          return (
            <SidebarMenuItem key={item.to}>
              <SidebarMenuButton asChild tooltip={item.label}>
                <Link
                  to={item.to}
                  activeProps={{ "data-active": "true" }}
                  // Workspace/admin items are section roots; highlight on the
                  // whole subtree (e.g. /user/sessions/:id) not just exact.
                  activeOptions={{ exact: false }}
                >
                  <Icon aria-hidden />
                  <span>{item.label}</span>
                </Link>
              </SidebarMenuButton>
            </SidebarMenuItem>
          );
        })}
      </SidebarMenu>
    </SidebarGroup>
  );
}

// The open session's own surfaces, titled with its label. Shares the session
// query (same key) with the overview, so it never adds a distinct request.
function SessionNavGroup({ sessionId }: { sessionId: string }) {
  const session = useSession(sessionId);
  const name = session.data?.label || sessionId;
  return (
    <SidebarGroup className="animate-in duration-300 ease-soft fade-in-0 slide-in-from-left-1">
      <SidebarGroupLabel title={name} className="gap-2">
        <span className="truncate">{name}</span>
      </SidebarGroupLabel>
      <SidebarMenu>
        {SESSION_NAV.map((item) => {
          const Icon = item.icon;
          return (
            <SidebarMenuItem key={item.to}>
              <SidebarMenuButton asChild tooltip={item.label}>
                <Link
                  to={item.to}
                  params={{ sessionId }}
                  activeProps={{ "data-active": "true" }}
                  activeOptions={{ exact: item.exact }}
                >
                  <Icon aria-hidden />
                  <span>{item.label}</span>
                </Link>
              </SidebarMenuButton>
            </SidebarMenuItem>
          );
        })}
      </SidebarMenu>
    </SidebarGroup>
  );
}
