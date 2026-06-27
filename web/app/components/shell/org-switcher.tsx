// Org switcher (§12 user surface): pick the active organization. better-auth's
// organization plugin stores the active org on the SESSION; the gateway JWT's
// `activeOrganizationId` claim (definePayload, §4.1) is what scopes every
// gateway call, so switching orgs must:
//   1. authClient.organization.setActive({ organizationId })  — server session
//   2. clear the cached gateway JWT so the next call mints one with the NEW
//      activeOrganizationId claim (token-provider, §4.7)
//   3. drop org-scoped TanStack Query caches (sessions/keys/webhooks are all
//      filtered by the active org gateway-side)
//   4. router.invalidate() so the _app beforeLoad re-resolves AppSession.activeOrg
//
// Reactive org list/active come from the better-auth client hooks
// (useListOrganizations / useActiveOrganization), which the organizationClient
// plugin exposes (atoms -> use<Atom> hooks).

import { useState } from "react";
import { useRouter } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";
import { BuildingIcon, CheckIcon, ChevronsUpDownIcon } from "lucide-react";
import { authClient } from "~/lib/auth/client";
import { clearGatewayToken } from "~/lib/api/token-provider";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "~/components/ui/dropdown-menu";
import { Button } from "~/components/ui/button";
import { toast } from "sonner";

export function OrgSwitcher() {
  const router = useRouter();
  const qc = useQueryClient();
  const orgs = authClient.useListOrganizations();
  const active = authClient.useActiveOrganization();
  const [switching, setSwitching] = useState<string | null>(null);

  const list = orgs.data ?? [];
  const activeOrg = active.data ?? null;
  const activeName = activeOrg?.name ?? "No organization";

  const select = async (organizationId: string): Promise<void> => {
    if (organizationId === activeOrg?.id) return;
    setSwitching(organizationId);
    try {
      const { error } = await authClient.organization.setActive({
        organizationId,
      });
      if (error) {
        toast.error(error.message ?? "Could not switch organization");
        return;
      }
      // The active org changed: the gateway JWT, all org-scoped query caches,
      // and the server-resolved AppSession are now stale.
      clearGatewayToken();
      qc.clear();
      await router.invalidate();
      toast.success("Switched organization");
    } catch {
      toast.error("Could not switch organization");
    } finally {
      setSwitching(null);
    }
  };

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="outline"
          size="sm"
          className="gap-2"
          disabled={orgs.isPending}
        >
          <BuildingIcon className="size-4" aria-hidden />
          <span className="max-w-40 truncate">{activeName}</span>
          <ChevronsUpDownIcon className="size-4 opacity-60" aria-hidden />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-56">
        <DropdownMenuLabel>Organizations</DropdownMenuLabel>
        <DropdownMenuSeparator />
        {list.length === 0 ? (
          <DropdownMenuItem disabled>No organizations</DropdownMenuItem>
        ) : (
          list.map((org) => (
            <DropdownMenuItem
              key={org.id}
              disabled={switching !== null}
              onSelect={(e) => {
                e.preventDefault();
                void select(org.id);
              }}
            >
              <span className="flex-1 truncate">{org.name}</span>
              {org.id === activeOrg?.id && (
                <CheckIcon className="size-4" aria-hidden />
              )}
            </DropdownMenuItem>
          ))
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
