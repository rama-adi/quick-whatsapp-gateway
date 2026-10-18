// The application sidebar, built on the shadcn `sidebar` primitive (collapsing
// to icons, the mobile Sheet drawer, Cmd/Ctrl+B, and tooltips come for free).
//
// It sits directly on the shell frame (AppShell renders it with
// variant="inset"): brand row on top, the role-filtered nav model
// (shell/nav.ts) in the middle, and the docs link + account chip at the
// bottom. It never changes shape with the route: one session's sections
// (Overview / Chats / Contacts) are tabs in that session's own page chrome
// (user/sessions.$sessionId.tsx), and "Sessions" stays the active item there.

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
