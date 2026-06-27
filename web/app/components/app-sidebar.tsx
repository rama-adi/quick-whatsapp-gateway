// The application sidebar. Replaces the v1 hand-rolled <aside> in AppShell with
// the shadcn `sidebar` primitive (dashboard-01), so we inherit collapsing,
// the mobile Sheet drawer, keyboard toggle (Cmd/Ctrl+B), and tooltips for free.
//
// Driven by the role-filtered nav model (shell/nav.ts) + the resolved
// AppSession. Boilerplate from the dashboard-01 block (Quick Create, demo
// nav data, documents/clouds groups) was removed; what remains is wired to
// this app's actual surfaces.

import { Link } from "@tanstack/react-router";
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
import { visibleNav, DOCS_NAV, type NavItem } from "./shell/nav";
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

  return (
    <Sidebar collapsible="icon" {...props}>
      <SidebarHeader>
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton asChild className="data-[slot=sidebar-menu-button]:!p-1.5">
              <Link to="/">
                <MessageSquareText className="!size-5" aria-hidden />
                <span className="text-base font-semibold">WA Gateway</span>
              </Link>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarHeader>

      <SidebarContent>
        {workspaceItems.length > 0 && (
          <NavGroup label="Workspace" items={workspaceItems} />
        )}
        {adminItems.length > 0 && <NavGroup label="Admin" items={adminItems} />}
      </SidebarContent>

      <SidebarFooter>
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
