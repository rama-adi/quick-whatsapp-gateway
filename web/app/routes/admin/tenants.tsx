// Admin: tenants list / ban / impersonate via Authula admin (/auth/admin/*).
// Surface: admin.

import { useMemo, useState } from "react";
import { requireAdmin } from "./_guard";
import { fmtTime } from "./_shared";
import { useTenants, useBanTenant, useImpersonate, type Tenant } from "~/lib/auth/admin";
import { isApiError } from "~/lib/api/envelope";
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
import { toast } from "sonner";

export const clientLoader = requireAdmin;

type Pending = { tenant: Tenant; ban: boolean } | null;

export default function AdminTenants() {
  const q = useTenants();
  const ban = useBanTenant();
  const impersonate = useImpersonate();
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
        onError: (err) =>
          toast.error(isApiError(err) ? err.message : "Action failed"),
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
        onError: (err) =>
          toast.error(isApiError(err) ? err.message : "Impersonation failed"),
      },
    );
  };

  return (
    <div className="space-y-4">
      <div>
        <h1 className="text-xl font-semibold">Tenants</h1>
        <p className="text-sm text-muted-foreground">
          Manage tenant accounts: ban access or impersonate for support.
        </p>
      </div>

      <Card>
        <CardHeader className="gap-3">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div>
              <CardTitle className="text-base">Accounts</CardTitle>
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
              aria-label="Search tenants"
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
              <p className="text-sm text-destructive">
                {isApiError(q.error) ? q.error.message : "Failed to load tenants"}
              </p>
              <Button variant="outline" size="sm" onClick={() => void q.refetch()}>
                Retry
              </Button>
            </div>
          ) : tenants.length === 0 ? (
            <p className="py-8 text-center text-sm text-muted-foreground">
              {search ? "No tenants match your search." : "No tenants found."}
            </p>
          ) : (
            <div className="overflow-x-auto">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Tenant</TableHead>
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
            <DialogTitle>{pending?.ban ? "Ban tenant?" : "Unban tenant?"}</DialogTitle>
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
    </div>
  );
}

function label(t: Tenant): string {
  return t.name || t.email || t.id;
}

function toEpoch(v: string | number | undefined): number | undefined {
  if (v === undefined) return undefined;
  if (typeof v === "number") return v;
  const parsed = Date.parse(v);
  return Number.isNaN(parsed) ? undefined : parsed;
}
