// Account dropdown: shows roles + sign-out.
// FROZEN — owned by the foundation agent.

import { useNavigate } from "react-router";
import { LogOut } from "lucide-react";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "~/components/ui/dropdown-menu";
import { Avatar, AvatarFallback } from "~/components/ui/avatar";
import { signOut } from "~/lib/auth/client";
import { queryClient } from "~/lib/query";
import type { AppSession } from "~/lib/auth/session";

export function UserMenu({ session }: { session: AppSession }) {
  const navigate = useNavigate();
  const initial = (session.user.email || session.user.roles[0] || "?")
    .charAt(0)
    .toUpperCase();

  const onSignOut = async (): Promise<void> => {
    try {
      await signOut();
    } finally {
      queryClient.clear();
      navigate("/login", { replace: true });
    }
  };

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button type="button" className="rounded-full outline-none focus-visible:ring-2 focus-visible:ring-ring">
          <Avatar className="size-8">
            <AvatarFallback>{initial}</AvatarFallback>
          </Avatar>
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-56">
        <DropdownMenuLabel>
          <div className="flex flex-col">
            <span className="truncate text-sm font-medium">
              {session.user.email || "Account"}
            </span>
            <span className="text-xs text-muted-foreground">
              {session.user.roles.join(", ") || "user"}
            </span>
          </div>
        </DropdownMenuLabel>
        <DropdownMenuSeparator />
        <DropdownMenuItem onSelect={() => void onSignOut()}>
          <LogOut className="mr-2 size-4" />
          Sign out
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
