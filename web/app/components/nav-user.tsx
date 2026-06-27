// Sidebar footer account menu. Tailored from the dashboard-01 `nav-user` block:
// the demo {name,email,avatar} + Account/Billing/Notifications items were
// replaced with the real AppSession (email + roles), a Documentation link, and
// the sign-out flow ported from the old shell/UserMenu.tsx (clears the query
// cache, then navigates to /login).

import { useNavigate } from "@tanstack/react-router";
import {
  Avatar,
  AvatarFallback,
} from "~/components/ui/avatar";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "~/components/ui/dropdown-menu";
import {
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  useSidebar,
} from "~/components/ui/sidebar";
import { BookOpen, EllipsisVerticalIcon, LogOut } from "lucide-react";
import { signOut } from "~/lib/auth/client";
import { queryClient } from "~/lib/query";
import type { AppSession } from "~/lib/auth/session";

export function NavUser({ session }: { session: AppSession }) {
  const { isMobile } = useSidebar();
  const navigate = useNavigate();

  const email = session.user.email || "Account";
  const roles = session.user.roles.join(", ") || "user";
  const initial = (session.user.email || session.user.roles[0] || "?")
    .charAt(0)
    .toUpperCase();

  const onSignOut = async (): Promise<void> => {
    try {
      await signOut();
    } finally {
      queryClient.clear();
      void navigate({ to: "/login", replace: true });
    }
  };

  return (
    <SidebarMenu>
      <SidebarMenuItem>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <SidebarMenuButton
              size="lg"
              className="data-[state=open]:bg-sidebar-accent data-[state=open]:text-sidebar-accent-foreground"
            >
              <Avatar className="size-8 rounded-lg">
                <AvatarFallback className="rounded-lg">{initial}</AvatarFallback>
              </Avatar>
              <div className="grid flex-1 text-left text-sm leading-tight">
                <span className="truncate font-medium">{email}</span>
                <span className="truncate text-xs text-muted-foreground">
                  {roles}
                </span>
              </div>
              <EllipsisVerticalIcon className="ml-auto size-4" />
            </SidebarMenuButton>
          </DropdownMenuTrigger>
          <DropdownMenuContent
            className="w-(--radix-dropdown-menu-trigger-width) min-w-56 rounded-lg"
            side={isMobile ? "bottom" : "right"}
            align="end"
            sideOffset={4}
          >
            <DropdownMenuLabel className="p-0 font-normal">
              <div className="flex flex-col px-1 py-1.5 text-left text-sm">
                <span className="truncate font-medium">{email}</span>
                <span className="truncate text-xs text-muted-foreground">
                  {roles}
                </span>
              </div>
            </DropdownMenuLabel>
            <DropdownMenuSeparator />
            <DropdownMenuGroup>
              <DropdownMenuItem asChild>
                <a href="/docs">
                  <BookOpen className="mr-2 size-4" />
                  Documentation
                </a>
              </DropdownMenuItem>
            </DropdownMenuGroup>
            <DropdownMenuSeparator />
            <DropdownMenuItem onSelect={() => void onSignOut()}>
              <LogOut className="mr-2 size-4" />
              Sign out
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </SidebarMenuItem>
    </SidebarMenu>
  );
}
