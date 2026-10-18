// Sidebar footer account chip (styled like naivault's vault switcher: a soft
// tinted card with a 2-line label: name, then email). Tailored from the
// dashboard-01 `nav-user` block: the demo Account/Billing/Notifications items
// were replaced with the real AppSession, a "Platform admin" marker for
// super admins (plain members get no role line), and the sign-out flow ported
// from the old shell/UserMenu.tsx (clears the query cache, then navigates to
// /login). Documentation lives in the sidebar footer only, not in this menu.

import { useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";
import {
  Avatar,
  AvatarFallback,
} from "~/components/ui/avatar";
import {
  DropdownMenu,
  DropdownMenuContent,
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
import { ChevronsUpDownIcon, LogOut, ShieldCheckIcon } from "lucide-react";
import { signOut } from "~/lib/auth/client";
import { clearGatewayToken } from "~/lib/api/token-provider";
import { formatPlatformRole } from "~/lib/format";
import type { AppSession } from "~/lib/auth/session";
import { toast } from "sonner";

export function NavUser({ session }: { session: AppSession }) {
  const { isMobile } = useSidebar();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [signingOut, setSigningOut] = useState(false);

  const { name, email } = session.user;
  // Line 1 is the name (falls back to the email); line 2 is the email when a
  // distinct name exists.
  const primary = name || email || "Account";
  const secondary = name && email && name !== email ? email : null;
  const isPlatformAdmin = session.user.roles.includes("super_admin");
  const initial = primary.charAt(0).toUpperCase() || "?";

  const onSignOut = async (): Promise<void> => {
    if (signingOut) return;
    setSigningOut(true);
    try {
      const { error } = await signOut();
      if (error) {
        toast.error(error.message ?? "Could not sign out");
        return;
      }
      clearGatewayToken();
      queryClient.clear();
      void navigate({ to: "/login", replace: true });
    } catch {
      toast.error("Could not sign out");
    } finally {
      setSigningOut(false);
    }
  };

  return (
    <SidebarMenu>
      <SidebarMenuItem>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <SidebarMenuButton
              size="lg"
              className="h-auto gap-2 bg-sidebar-accent/50 px-2 py-1.5 text-sidebar-foreground hover:bg-sidebar-accent data-[state=open]:bg-sidebar-accent group-data-[collapsible=icon]:bg-transparent group-data-[collapsible=icon]:p-0.5!"
            >
              <Avatar className="size-7 rounded-lg">
                <AvatarFallback className="rounded-lg bg-background text-xs font-semibold text-foreground">
                  {initial}
                </AvatarFallback>
              </Avatar>
              <div className="grid min-w-0 flex-1 text-left leading-tight">
                <span className="truncate text-sm font-medium">{primary}</span>
                {(secondary || isPlatformAdmin) && (
                  <span className="truncate text-xs text-muted-foreground">
                    {secondary ?? formatPlatformRole("super_admin")}
                  </span>
                )}
              </div>
              <ChevronsUpDownIcon
                aria-hidden
                className="ml-auto size-3.5! text-muted-foreground transition-colors duration-200 ease-soft group-hover/menu-button:text-sidebar-foreground"
              />
            </SidebarMenuButton>
          </DropdownMenuTrigger>
          <DropdownMenuContent
            className="w-(--radix-dropdown-menu-trigger-width) min-w-56"
            side={isMobile ? "bottom" : "right"}
            align="end"
            sideOffset={4}
          >
            <DropdownMenuLabel className="p-0 font-normal">
              <div className="flex flex-col gap-0.5 px-2 py-1.5 text-left text-sm">
                <span className="truncate font-medium text-foreground">{primary}</span>
                {secondary && (
                  <span className="truncate text-xs text-muted-foreground">
                    {secondary}
                  </span>
                )}
                {isPlatformAdmin && (
                  <span className="mt-1 inline-flex w-fit items-center gap-1 rounded-full bg-muted px-2 py-0.5 text-[0.7rem] font-medium text-muted-foreground">
                    <ShieldCheckIcon className="size-3" aria-hidden />
                    {formatPlatformRole("super_admin")}
                  </span>
                )}
              </div>
            </DropdownMenuLabel>
            <DropdownMenuSeparator />
            <DropdownMenuItem
              disabled={signingOut}
              onSelect={() => void onSignOut()}
            >
              <LogOut aria-hidden />
              {signingOut ? "Signing out…" : "Sign out"}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </SidebarMenuItem>
    </SidebarMenu>
  );
}
