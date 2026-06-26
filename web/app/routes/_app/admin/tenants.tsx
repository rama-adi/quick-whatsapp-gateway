// Admin: platform users + organizations management.
//
// v2 replacement for the v1 admin/tenants.tsx (which hit the Authula admin
// plugin via the removed ~/lib/auth/admin). Now backed by the better-auth ADMIN
// client (list/ban/unban/impersonate/setRole) and ORGANIZATION client (org
// list) through the colocated ./-admin-client hooks.
//
// Route note: the FROZEN app shell nav (app/components/shell/nav.ts) links the
// admin "Tenants" item at /admin/tenants and has no separate /admin/users or
// /admin/orgs entries. The masterplan §-Stage-3 brief names those two surfaces;
// to satisfy both without editing the frozen nav, this single /admin/tenants
// page hosts BOTH as tabs (Users + Organizations). See sharedGaps in the return
// notes if the Verify stage prefers to split them into two routes + nav items.
//
// Guard: the parent /admin route's beforeLoad already gates super_admin.

import { useMemo, useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { fmtTime } from "./-shared";
import {
  useTenants,
  useBanTenant,
  useImpersonate,
  useSetRole,
  useOrgs,
  type Tenant,
  type Org,
} from "./-admin-client";
import { Button } from "~/components/ui/button";
import { Badge } from "~/components/ui/badge";
import { Input } from "~/components/ui/input";
import { Skeleton } from "~/components/ui/skeleton";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "~/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "~/components/ui/table";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "~/components/ui/dialog";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "~/components/ui/tabs";
import { toast } from "sonner";

export const Route = createFileRoute("/_app/admin/tenants")({
  component: AdminTenants,
});

function errMsg(err: unknown): string {
  return err instanceof Error ? err.message : "Action failed";
}

function toEpoch(v: string | number | Date | undefined): number | undefined {
  if (v === undefined) return undefined;
  if (typeof v === "number") return v;
  const parsed = v instanceof Date ? v.getTime() : Date.parse(v);
  return Number.isNaN(parsed) ? undefined : parsed;
}

function AdminTenants() {
  return (
    <div className="space-y-4">
      <div>
        <h1 className="text-xl font-semibold">Tenants</h1>
        <p className="text-sm text-muted-foreground">
          Manage platform users (ban / impersonate / role) and view organizations.
        </p>
      </div>

      <Tabs defaultValue="users">
        <TabsList>
          <TabsTrigger value="users">Users</TabsTrigger>
          <TabsTrigger value="orgs">Organizations</TabsTrigger>
        </TabsList>
        <TabsContent value="users" className="mt-4">
          <UsersPanel />
        </TabsContent>
        <TabsContent value="orgs" className="mt-4">
          <OrgsPanel />
        </TabsContent>
      </Tabs>
    </div>
  );
}

type Pending = { tenant: Tenant; ban: boolean } | null;

function label(t: Tenant): string {
  return t.name || t.email || t.id;
}

function UsersPanel() {
  const q = useTenants();
  const ban = useBanTenant();
  const impersonate = useImpersonate();
  const setRole = useSetRole();
  const [search, setSearch] = useState("");
  const [pending, setPending] = useState<Pending>(null);

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

  const confirmBan = (): void => {
    if (!pending) return;
    const { tenant, ban: shouldBan } = pending;
    ban.mutate(
      { id: tenant.id, ban: shouldBan },
      {
        onSuccess: () =>
          toast.success(`${shouldBan ? "Banned" : "Unbanned"} ${label(tenant)}`),
        onError: (err) => toast.error(errMsg(err)),
        onSettled: () => setPending(null),
      },
    );
  };

  const runImpersonate = (tenant: Tenant): void => {
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

  const toggleAdmin = (tenant: Tenant): void => {
    const next = tenant.role === "super_admin" ? "user" : "super_admin";
    setRole.mutate(
      { id: tenant.id, role: next },
      {
        onSuccess: () => toast.success(`${label(tenant)} role → ${next}`),
        onError: (err) => toast.error(errMsg(err)),
      },
    );
  };

  return (
    <>
      <Card>
        <CardHeader className="gap-3">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div>
              <CardTitle className="text-base">Users</CardTitle>
              <CardDescription>
                {q.isSuccess ? `${tenants.length} shown` : "Loading…"}
              </CardDescription>
            </div>
            <Input
              type="search"
              placeholder="Search email, name, id…"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              className="w-full sm:w-64"
              aria-label="Search users"
            />
          </div>
        </CardHeader>
        <CardContent>
          {q.isLoading ? (
            <div className="space-y-2">
              <Skeleton className="h-10 w-full" />
              <Skeleton className="h-10 w-full" />
              <Skeleton className="h-10 w-full" />
            </div>
          ) : q.isError ? (
            <div className="space-y-3 py-6 text-center">
              <p className="text-sm text-destructive">{errMsg(q.error)}</p>
              <Button variant="outline" size="sm" onClick={() => void q.refetch()}>
                Retry
              </Button>
            </div>
          ) : tenants.length === 0 ? (
            <p className="py-8 text-center text-sm text-muted-foreground">
              {search ? "No users match your search." : "No users found."}
            </p>
          ) : (
            <div className="overflow-x-auto">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>User</TableHead>
                    <TableHead>Role</TableHead>
                    <TableHead>Status</TableHead>
                    <TableHead>Created</TableHead>
                    <TableHead className="text-right">Actions</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {tenants.map((t) => (
                    <TableRow key={t.id}>
                      <TableCell>
                        <div className="font-medium">{t.name || t.email || "(no name)"}</div>
                        {t.email && t.name && (
                          <div className="text-xs text-muted-foreground">{t.email}</div>
                        )}
                        <div className="font-mono text-xs text-muted-foreground">{t.id}</div>
                      </TableCell>
                      <TableCell>
                        {t.role === "super_admin" ? (
                          <Badge variant="default">super_admin</Badge>
                        ) : (
                          <Badge variant="outline">{t.role || "user"}</Badge>
                        )}
                      </TableCell>
                      <TableCell>
                        {t.banned ? (
                          <Badge variant="destructive">banned</Badge>
                        ) : (
                          <Badge variant="secondary">active</Badge>
                        )}
                      </TableCell>
                      <TableCell className="text-sm text-muted-foreground">
                        {fmtTime(toEpoch(t.createdAt))}
                      </TableCell>
                      <TableCell className="text-right">
                        <div className="flex justify-end gap-2">
                          <Button
                            variant="ghost"
                            size="sm"
                            disabled={setRole.isPending}
                            onClick={() => toggleAdmin(t)}
                          >
                            {t.role === "super_admin" ? "Demote" : "Make admin"}
                          </Button>
                          <Button
                            variant="outline"
                            size="sm"
                            disabled={impersonate.isPending}
                            onClick={() => runImpersonate(t)}
                          >
                            Impersonate
                          </Button>
                          <Button
                            variant={t.banned ? "outline" : "destructive"}
                            size="sm"
                            disabled={ban.isPending}
                            onClick={() => setPending({ tenant: t, ban: !t.banned })}
                          >
                            {t.banned ? "Unban" : "Ban"}
                          </Button>
                        </div>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          )}
        </CardContent>
      </Card>

      <Dialog open={pending !== null} onOpenChange={(o) => !o && setPending(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{pending?.ban ? "Ban user?" : "Unban user?"}</DialogTitle>
            <DialogDescription>
              {pending?.ban
                ? `${pending ? label(pending.tenant) : ""} will lose access immediately.`
                : `${pending ? label(pending.tenant) : ""} will regain access.`}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <DialogClose asChild>
              <Button variant="outline" disabled={ban.isPending}>
                Cancel
              </Button>
            </DialogClose>
            <Button
              variant={pending?.ban ? "destructive" : "default"}
              disabled={ban.isPending}
              onClick={confirmBan}
            >
              {ban.isPending ? "Working…" : pending?.ban ? "Ban" : "Unban"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
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
    <Card>
      <CardHeader className="gap-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div>
            <CardTitle className="text-base">Organizations</CardTitle>
            <CardDescription>
              {q.isSuccess ? `${orgs.length} shown` : "Loading…"}
            </CardDescription>
          </div>
          <Input
            type="search"
            placeholder="Search name, slug, id…"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            className="w-full sm:w-64"
            aria-label="Search organizations"
          />
        </div>
      </CardHeader>
      <CardContent>
        {q.isLoading ? (
          <div className="space-y-2">
            <Skeleton className="h-10 w-full" />
            <Skeleton className="h-10 w-full" />
          </div>
        ) : q.isError ? (
          <div className="space-y-3 py-6 text-center">
            <p className="text-sm text-destructive">{errMsg(q.error)}</p>
            <Button variant="outline" size="sm" onClick={() => void q.refetch()}>
              Retry
            </Button>
          </div>
        ) : orgs.length === 0 ? (
          <p className="py-8 text-center text-sm text-muted-foreground">
            {search ? "No organizations match your search." : "No organizations found."}
          </p>
        ) : (
          <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Organization</TableHead>
                  <TableHead>Slug</TableHead>
                  <TableHead className="text-right">Created</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {orgs.map((o) => (
                  <TableRow key={o.id}>
                    <TableCell>
                      <div className="font-medium">{o.name}</div>
                      <div className="font-mono text-xs text-muted-foreground">{o.id}</div>
                    </TableCell>
                    <TableCell className="text-sm text-muted-foreground">
                      {o.slug || "—"}
                    </TableCell>
                    <TableCell className="text-right text-sm text-muted-foreground">
                      {fmtTime(toEpoch(o.createdAt))}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )}
      </CardContent>
    </Card>
  );
}
