// Admin: platform users + organizations ("Users & orgs").
//
// Backed by the better-auth ADMIN client (list/ban/unban/impersonate/setRole)
// and ORGANIZATION client (org list) through the colocated ./-admin-client
// hooks. Every privileged action asks for confirmation, and none of them can
// be run on your own account. The active tab lives in `?tab=`.
//
// Guard: the parent /admin route's beforeLoad already gates super_admin.

import { useMemo, useState } from "react";
import { Link, createFileRoute, useNavigate } from "@tanstack/react-router";
import {
  BuildingIcon,
  EllipsisIcon,
  RefreshCwIcon,
  SearchIcon,
  ShieldIcon,
  ShieldOffIcon,
  UserRoundCogIcon,
  UsersIcon,
} from "lucide-react";
import {
  useTenants,
  useBanTenant,
  useImpersonate,
  useSetRole,
  useOrgs,
  type Tenant,
  type Org,
} from "./-admin-client";
import { useAppSession } from "~/lib/auth/context";
import { Button } from "~/components/ui/button";
import { Badge } from "~/components/ui/badge";
import { Input } from "~/components/ui/input";
import { Skeleton } from "~/components/ui/skeleton";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "~/components/ui/dropdown-menu";
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "~/components/ui/empty";
import {
  Table,
  TableBody,
  TableCard,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "~/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "~/components/ui/tabs";
import { Page, PageTitle } from "~/components/shell/page-title";
import { useConfirm } from "~/components/confirm-dialog";
import { CopyableId } from "~/components/copyable";
import { Time } from "~/components/time";
import { formatPlatformRole } from "~/lib/format";
import { titleHead } from "~/lib/head";
import { DOCS } from "~/lib/docs-links";
import { toast } from "sonner";

type Tab = "users" | "orgs";

export const Route = createFileRoute("/_app/admin/tenants")({
  validateSearch: (search: Record<string, unknown>): { tab?: Tab } => ({
    tab: search.tab === "orgs" ? "orgs" : undefined,
  }),
  head: () => titleHead("Users & orgs"),
  component: AdminTenants,
});

function errMsg(err: unknown): string {
  return err instanceof Error ? err.message : "Action failed";
}

function AdminTenants() {
  const { tab = "users" } = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });

  return (
    <Page>
      <PageTitle
        title="Users & orgs"
        description="Every account and organization on this platform. Change roles, ban accounts, or sign in as someone to help them."
        docsHref={DOCS.organizations}
      />

      <Tabs
        value={tab}
        onValueChange={(next) =>
          void navigate({ search: { tab: next === "orgs" ? "orgs" : undefined }, replace: true })
        }
        className="gap-4"
      >
        <TabsList>
          <TabsTrigger value="users">Users</TabsTrigger>
          <TabsTrigger value="orgs">Organizations</TabsTrigger>
        </TabsList>
        <TabsContent value="users">
          <UsersPanel />
        </TabsContent>
        <TabsContent value="orgs">
          <OrgsPanel />
        </TabsContent>
      </Tabs>
    </Page>
  );
}

function label(t: Tenant): string {
  return t.name || t.email || t.id;
}

function SearchBox({
  value,
  onChange,
  placeholder,
  ariaLabel,
  count,
}: {
  value: string;
  onChange: (value: string) => void;
  placeholder: string;
  ariaLabel: string;
  count?: string;
}) {
  return (
    <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
      <div className="relative w-full sm:w-72">
        <SearchIcon className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" aria-hidden />
        <Input
          type="search"
          placeholder={placeholder}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          className="pl-8"
          aria-label={ariaLabel}
        />
      </div>
      {count ? <span className="text-sm text-muted-foreground">{count}</span> : null}
    </div>
  );
}

function LoadingRows() {
  return (
    <div className="space-y-2 p-4">
      <Skeleton className="h-12 w-full" />
      <Skeleton className="h-12 w-full" />
      <Skeleton className="h-12 w-full" />
    </div>
  );
}

function ErrorRows({ error, onRetry }: { error: unknown; onRetry: () => void }) {
  return (
    <div className="flex flex-col items-center gap-3 py-10 text-center">
      <p className="text-sm text-destructive">{errMsg(error)}</p>
      <Button variant="outline" size="sm" onClick={onRetry}>
        <RefreshCwIcon aria-hidden />
        Retry
      </Button>
    </div>
  );
}

function UsersPanel() {
  const q = useTenants();
  const ban = useBanTenant();
  const impersonate = useImpersonate();
  const setRole = useSetRole();
  const confirm = useConfirm();
  const me = useAppSession().user.id;
  const [search, setSearch] = useState("");

  const tenants = useMemo(() => {
    const all = q.data ?? [];
    const needle = search.trim().toLowerCase();
    if (!needle) return all;
    return all.filter((t) =>
      [t.email, t.name, t.id]
        .filter((v): v is string => typeof v === "string")
        .some((v) => v.toLowerCase().includes(needle)),
    );
  }, [q.data, search]);

  const toggleBan = async (tenant: Tenant) => {
    const shouldBan = !tenant.banned;
    const ok = await confirm(
      shouldBan
        ? {
            title: <>Ban {label(tenant)}?</>,
            description:
              "They're signed out everywhere and can't sign in again until you unban them.",
            confirmLabel: "Ban user",
            destructive: true,
          }
        : {
            title: <>Unban {label(tenant)}?</>,
            description: "They can sign in again right away.",
            confirmLabel: "Unban user",
          },
    );
    if (!ok) return;
    ban.mutate(
      { id: tenant.id, ban: shouldBan },
      {
        onSuccess: () => toast.success(`${shouldBan ? "Banned" : "Unbanned"} ${label(tenant)}`),
        onError: (err) => toast.error(errMsg(err)),
      },
    );
  };

  const runImpersonate = async (tenant: Tenant) => {
    const ok = await confirm({
      title: <>Sign in as {label(tenant)}?</>,
      description:
        "You'll see the dashboard exactly as they do and act with their permissions. Use “Stop impersonating” in the top bar to return to your own account.",
      confirmLabel: "Impersonate",
    });
    if (!ok) return;
    impersonate.mutate(
      { userId: tenant.id },
      {
        onSuccess: () => {
          toast.success(`Impersonating ${label(tenant)}`);
          // Identity switched; send the operator to the workspace as the target.
          window.location.assign("/");
        },
        onError: (err) => toast.error(errMsg(err)),
      },
    );
  };

  const toggleAdmin = async (tenant: Tenant) => {
    const promote = tenant.role !== "super_admin";
    const ok = await confirm(
      promote
        ? {
            title: <>Make {label(tenant)} a platform admin?</>,
            description:
              "They get full control of the platform: every organization's sessions, all users, gateways and impersonation.",
            confirmLabel: "Make platform admin",
            destructive: true,
          }
        : {
            title: <>Remove platform admin from {label(tenant)}?</>,
            description:
              "They lose access to the admin pages and keep only their own organization memberships.",
            confirmLabel: "Remove admin",
            destructive: true,
          },
    );
    if (!ok) return;
    const next = promote ? "super_admin" : "user";
    setRole.mutate(
      { id: tenant.id, role: next },
      {
        onSuccess: () => toast.success(`${label(tenant)} is now ${promote ? "a platform admin" : "a member"}`),
        onError: (err) => toast.error(errMsg(err)),
      },
    );
  };

  return (
    <div className="grid gap-3">
      <SearchBox
        value={search}
        onChange={setSearch}
        placeholder="Search email, name, ID…"
        ariaLabel="Search users"
        count={q.isSuccess ? `${tenants.length} user${tenants.length === 1 ? "" : "s"}` : undefined}
      />
      <TableCard>
        {q.isLoading ? (
          <LoadingRows />
        ) : q.isError ? (
          <ErrorRows error={q.error} onRetry={() => void q.refetch()} />
        ) : tenants.length === 0 ? (
          <Empty className="border-0">
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <UsersIcon aria-hidden />
              </EmptyMedia>
              <EmptyTitle>{search ? "No users match" : "No users yet"}</EmptyTitle>
              <EmptyDescription>
                {search ? "Try a different name, email or ID." : "People appear here after they register."}
              </EmptyDescription>
            </EmptyHeader>
          </Empty>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>User</TableHead>
                <TableHead>Role</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>Joined</TableHead>
                <TableHead className="text-right">
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {tenants.map((t) => {
                const self = t.id === me;
                const isAdmin = t.role === "super_admin";
                return (
                  <TableRow key={t.id}>
                    <TableCell>
                      <div className="flex items-center gap-2 font-medium">
                        {t.name || t.email || "(no name)"}
                        {self ? <Badge variant="outline">You</Badge> : null}
                      </div>
                      {t.email && t.name ? (
                        <div className="text-xs text-muted-foreground">{t.email}</div>
                      ) : null}
                      <div className="text-muted-foreground">
                        <CopyableId value={t.id} truncate label="Copy user ID" />
                      </div>
                    </TableCell>
                    <TableCell>
                      {isAdmin ? (
                        <Badge variant="default" className="gap-1">
                          <ShieldIcon aria-hidden />
                          {formatPlatformRole(t.role)}
                        </Badge>
                      ) : (
                        <span className="text-muted-foreground">{formatPlatformRole(t.role)}</span>
                      )}
                    </TableCell>
                    <TableCell>
                      {t.banned ? (
                        <Badge variant="destructive" className="gap-1.5">
                          <span aria-hidden className="size-1.5 rounded-full bg-destructive" />
                          Banned
                        </Badge>
                      ) : (
                        <Badge className="gap-1.5 bg-emerald-500/12 text-emerald-700 dark:text-emerald-400">
                          <span aria-hidden className="size-1.5 rounded-full bg-emerald-500" />
                          Active
                        </Badge>
                      )}
                    </TableCell>
                    <TableCell className="text-muted-foreground">
                      <Time value={t.createdAt} />
                    </TableCell>
                    <TableCell className="text-right">
                      <div className="flex items-center justify-end gap-1">
                        <Button
                          variant="ghost"
                          size="sm"
                          className={t.banned ? undefined : "text-destructive hover:bg-destructive/10 hover:text-destructive"}
                          disabled={self || ban.isPending}
                          title={self ? "You can't ban yourself" : undefined}
                          onClick={() => void toggleBan(t)}
                        >
                          {t.banned ? "Unban" : "Ban"}
                        </Button>
                        <DropdownMenu>
                          <DropdownMenuTrigger asChild>
                            <Button
                              variant="ghost"
                              size="icon-sm"
                              aria-label={`More actions for ${label(t)}`}
                              disabled={self}
                              title={self ? "These actions aren't available on your own account" : undefined}
                            >
                              <EllipsisIcon aria-hidden />
                            </Button>
                          </DropdownMenuTrigger>
                          <DropdownMenuContent align="end" className="w-52">
                            <DropdownMenuItem
                              disabled={impersonate.isPending}
                              onSelect={() => void runImpersonate(t)}
                            >
                              <UserRoundCogIcon aria-hidden />
                              Impersonate
                            </DropdownMenuItem>
                            <DropdownMenuItem
                              disabled={setRole.isPending}
                              onSelect={() => void toggleAdmin(t)}
                            >
                              {isAdmin ? <ShieldOffIcon aria-hidden /> : <ShieldIcon aria-hidden />}
                              {isAdmin ? "Remove platform admin" : "Make platform admin"}
                            </DropdownMenuItem>
                          </DropdownMenuContent>
                        </DropdownMenu>
                      </div>
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        )}
      </TableCard>
    </div>
  );
}

function OrgsPanel() {
  const q = useOrgs();
  const [search, setSearch] = useState("");

  const orgs = useMemo<Org[]>(() => {
    const all = q.data ?? [];
    const needle = search.trim().toLowerCase();
    if (!needle) return all;
    return all.filter((o) =>
      [o.name, o.slug, o.id]
        .filter((v): v is string => typeof v === "string")
        .some((v) => v.toLowerCase().includes(needle)),
    );
  }, [q.data, search]);

  return (
    <div className="grid gap-3">
      <SearchBox
        value={search}
        onChange={setSearch}
        placeholder="Search name, slug, ID…"
        ariaLabel="Search organizations"
        count={q.isSuccess ? `${orgs.length} organization${orgs.length === 1 ? "" : "s"}` : undefined}
      />
      <TableCard>
        {q.isLoading ? (
          <LoadingRows />
        ) : q.isError ? (
          <ErrorRows error={q.error} onRetry={() => void q.refetch()} />
        ) : orgs.length === 0 ? (
          <Empty className="border-0">
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <BuildingIcon aria-hidden />
              </EmptyMedia>
              <EmptyTitle>{search ? "No organizations match" : "No organizations yet"}</EmptyTitle>
              <EmptyDescription>
                {search ? "Try a different name, slug or ID." : "Organizations appear here when users create them."}
              </EmptyDescription>
            </EmptyHeader>
          </Empty>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Organization</TableHead>
                <TableHead>Slug</TableHead>
                <TableHead>Created</TableHead>
                <TableHead className="text-right">
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {orgs.map((o) => (
                <TableRow key={o.id}>
                  <TableCell>
                    <div className="font-medium">{o.name}</div>
                    <div className="text-muted-foreground">
                      <CopyableId value={o.id} truncate label="Copy organization ID" />
                    </div>
                  </TableCell>
                  <TableCell className="font-mono text-xs text-muted-foreground">{o.slug || "—"}</TableCell>
                  <TableCell className="text-muted-foreground">
                    <Time value={o.createdAt} />
                  </TableCell>
                  <TableCell className="text-right">
                    <Button asChild variant="ghost" size="sm">
                      <Link to="/admin/sessions" search={{ org: o.id }}>
                        View sessions
                      </Link>
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </TableCard>
    </div>
  );
}
