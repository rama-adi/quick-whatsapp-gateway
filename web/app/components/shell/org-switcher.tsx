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
//
// The menu also offers "Create organization" (organization.create; the
// plugin's default lets any user create one, and creating makes it the active
// org, so it runs the same refresh steps 2-4), a disabled "Members" entry
// until member management has a UI, and a link to the organizations guide.
// With no active org the trigger reads "Select organization" and the menu
// explains what an organization is for.

import { useState } from "react";
import { useRouter } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";
import {
  ArrowUpRightIcon,
  BuildingIcon,
  CheckIcon,
  ChevronsUpDownIcon,
  Loader2,
  PlusIcon,
  UsersIcon,
} from "lucide-react";
import { authClient } from "~/lib/auth/client";
import { clearGatewayToken } from "~/lib/api/token-provider";
import { useSessionContext } from "~/lib/auth/context";
import { DOCS } from "~/lib/docs-links";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "~/components/ui/dropdown-menu";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "~/components/ui/dialog";
import { Button } from "~/components/ui/button";
import { Input } from "~/components/ui/input";
import { Label } from "~/components/ui/label";
import { cn } from "~/lib/utils";
import { toast } from "sonner";

/** Steps 2-4 above: everything org-scoped is stale once the active org changes. */
function useRefreshForActiveOrg(): () => Promise<void> {
  const router = useRouter();
  const qc = useQueryClient();
  return async () => {
    clearGatewayToken();
    qc.clear();
    await router.invalidate();
  };
}

export function OrgSwitcher() {
  const session = useSessionContext();
  const refresh = useRefreshForActiveOrg();
  const orgs = authClient.useListOrganizations();
  const active = authClient.useActiveOrganization();
  const [switching, setSwitching] = useState<string | null>(null);
  const [createOpen, setCreateOpen] = useState(false);

  const list = orgs.data ?? [];
  const activeOrg = active.data ?? null;
  const activeRole = activeOrg && session?.activeOrg?.id === activeOrg.id
    ? session.activeOrg.role
    : null;

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
      await refresh();
      const name = list.find((o) => o.id === organizationId)?.name;
      toast.success(name ? `Switched to ${name}` : "Switched organization");
    } catch {
      toast.error("Could not switch organization");
    } finally {
      setSwitching(null);
    }
  };

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button
            id="org-switcher"
            variant="ghost"
            size="sm"
            aria-label={
              activeOrg ? `Organization: ${activeOrg.name}. Switch organization` : "Select an organization"
            }
            className={cn(
              "h-8 max-w-56 gap-2 rounded-lg bg-muted/60 px-2 text-sm font-medium hover:bg-muted aria-expanded:bg-muted",
              !activeOrg && !orgs.isPending && "border-dashed border-border text-muted-foreground",
            )}
            disabled={orgs.isPending}
          >
            <OrgAvatar name={activeOrg?.name} />
            <span className="min-w-0 truncate">
              {activeOrg?.name ?? "Select organization"}
            </span>
            <ChevronsUpDownIcon className="size-3.5 text-muted-foreground" aria-hidden />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-72">
          <DropdownMenuLabel className="text-xs font-medium text-muted-foreground">
            Organizations
          </DropdownMenuLabel>
          {!activeOrg && (
            <p className="px-2 pb-2 text-xs text-pretty text-muted-foreground">
              Pick the organization you want to work in. Sessions, API keys and
              webhooks all belong to one organization.
            </p>
          )}
          {list.length === 0 ? (
            <DropdownMenuItem disabled>You&apos;re not in any organization yet</DropdownMenuItem>
          ) : (
            list.map((org) => {
              const isActive = org.id === activeOrg?.id;
              return (
                <DropdownMenuItem
                  key={org.id}
                  disabled={switching !== null}
                  onSelect={(e) => {
                    e.preventDefault();
                    void select(org.id);
                  }}
                  className="gap-2"
                >
                  <OrgAvatar name={org.name} />
                  <span className="grid min-w-0 flex-1 leading-tight">
                    <span className="truncate">{org.name}</span>
                    {isActive && activeRole ? (
                      <span className="truncate text-xs text-muted-foreground">
                        You&apos;re {roleLabel(activeRole)}
                      </span>
                    ) : null}
                  </span>
                  {switching === org.id ? (
                    <Loader2 className="size-4 animate-spin text-muted-foreground motion-reduce:animate-none" aria-hidden />
                  ) : isActive ? (
                    <CheckIcon className="size-4 text-foreground" aria-hidden />
                  ) : null}
                </DropdownMenuItem>
              );
            })
          )}
          <DropdownMenuSeparator />
          <DropdownMenuItem onSelect={() => setCreateOpen(true)}>
            <PlusIcon aria-hidden />
            Create organization
          </DropdownMenuItem>
          <DropdownMenuItem disabled>
            <UsersIcon aria-hidden />
            <span className="flex-1">Members</span>
            <span className="rounded-full bg-muted px-1.5 py-px text-[0.65rem] font-medium text-muted-foreground">
              Coming soon
            </span>
          </DropdownMenuItem>
          <DropdownMenuItem asChild>
            <a href={DOCS.organizations} target="_blank" rel="noreferrer">
              <ArrowUpRightIcon aria-hidden />
              How organizations work
            </a>
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>

      <CreateOrgDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
        onCreated={refresh}
      />
    </>
  );
}

function OrgAvatar({ name }: { name?: string | null }) {
  const initial = name?.trim().charAt(0).toUpperCase();
  return (
    <span
      aria-hidden
      className="flex size-5 shrink-0 items-center justify-center rounded-md bg-background text-[0.65rem] font-semibold text-muted-foreground shadow-xs ring-1 ring-border"
    >
      {initial || <BuildingIcon className="size-3" />}
    </span>
  );
}

function roleLabel(role: string): string {
  if (role === "owner") return "the owner";
  if (role === "admin") return "an admin";
  return "a member";
}

/** URL-safe slug from a display name plus a short random suffix (slugs must be unique). */
function makeSlug(name: string): string {
  const base = name
    .toLowerCase()
    .normalize("NFKD")
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 32);
  const suffix = Math.random().toString(36).slice(2, 8);
  return base ? `${base}-${suffix}` : `org-${suffix}`;
}

function CreateOrgDialog({
  open,
  onOpenChange,
  onCreated,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onCreated: () => Promise<void>;
}) {
  const [name, setName] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const changeOpen = (next: boolean): void => {
    if (busy) return;
    if (!next) {
      setName("");
      setError(null);
    }
    onOpenChange(next);
  };

  const submit = async (e: React.FormEvent): Promise<void> => {
    e.preventDefault();
    const trimmed = name.trim();
    if (!trimmed) {
      setError("Enter a name for the organization.");
      return;
    }
    if (trimmed.length > 80) {
      setError("Keep the name under 80 characters.");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      // Creating also makes it the active org (keepCurrentActiveOrganization
      // defaults to false), so refresh exactly like a switch.
      const { error: err } = await authClient.organization.create({
        name: trimmed,
        slug: makeSlug(trimmed),
      });
      if (err) {
        setError(err.message ?? "Could not create the organization.");
        return;
      }
      await onCreated();
      toast.success(`Created ${trimmed} and switched to it`);
      setName("");
      onOpenChange(false);
    } catch {
      setError("Could not create the organization.");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={changeOpen}>
      <DialogContent className="sm:max-w-md">
        <form onSubmit={(e) => void submit(e)} noValidate className="grid gap-5">
          <DialogHeader>
            <DialogTitle>Create organization</DialogTitle>
            <DialogDescription className="text-pretty">
              A separate workspace with its own WhatsApp numbers, API keys and
              webhooks. You&apos;ll be its owner, and the dashboard switches to it
              right away.
            </DialogDescription>
          </DialogHeader>
          <div className="grid gap-2">
            <Label htmlFor="org-name">Name</Label>
            <Input
              id="org-name"
              autoFocus
              autoComplete="organization"
              placeholder="Acme Support"
              value={name}
              maxLength={80}
              aria-invalid={error ? true : undefined}
              aria-describedby={error ? "org-name-error" : undefined}
              onChange={(e) => setName(e.target.value)}
            />
            {error ? (
              <p id="org-name-error" className="text-sm text-destructive">
                {error}
              </p>
            ) : (
              <p className="text-xs text-muted-foreground">
                Usually a company, client or project name. You can switch between
                organizations at any time.
              </p>
            )}
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => changeOpen(false)} disabled={busy}>
              Cancel
            </Button>
            <Button type="submit" disabled={busy}>
              {busy && <Loader2 className="size-4 animate-spin motion-reduce:animate-none" aria-hidden />}
              Create organization
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
