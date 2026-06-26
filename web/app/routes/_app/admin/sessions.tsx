// Admin: all WhatsApp sessions across ALL orgs, with live statuses.
// Gateway GET /admin/sessions via useAdminSessions (TanStack Query infinite).
// Live status overlay rides the shared NDJSON firehose (no second socket).
//
// Ported from v1 admin/sessions.tsx: clientLoader=requireAdmin -> the parent
// /admin route's super_admin beforeLoad; imports repointed to ./-shared.
//
// NOTE (R5): the masterplan §12 asks each row to show its gatewayId. The FROZEN
// generated OpenAPI schema (app/lib/api/schema.d.ts) still exposes only
// `tenantId` on WASession (no gatewayId field yet). We render `tenantId` (the
// owning org id in v2) under an "Org" column; once the gateway's OpenAPI is
// regenerated with `gatewayId`, swap the column to `s.gatewayId`.

import { useMemo } from "react";
import { createFileRoute } from "@tanstack/react-router";
import {
  SessionStatusBadge,
  StreamIndicator,
  useLiveSessionStatus,
  withLiveStatus,
  fmtTime,
} from "./-shared";
import { useAdminSessions } from "~/lib/api/hooks/admin";
import type { WASession } from "~/lib/api/types";
import { isApiError } from "~/lib/api/envelope";
import { Button } from "~/components/ui/button";
import { Badge } from "~/components/ui/badge";
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

export const Route = createFileRoute("/_app/admin/sessions")({
  component: AdminSessions,
});

function AdminSessions() {
  const q = useAdminSessions();
  const overrides = useLiveSessionStatus();

  const rows: WASession[] = useMemo(
    () => q.data?.pages.flatMap((p) => p.data ?? []) ?? [],
    [q.data],
  );

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h1 className="text-xl font-semibold">All Sessions</h1>
          <p className="text-sm text-muted-foreground">
            Cross-org WhatsApp sessions and their live status.
          </p>
        </div>
        <StreamIndicator />
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">Sessions</CardTitle>
          <CardDescription>
            {q.isSuccess ? `${rows.length} session${rows.length === 1 ? "" : "s"} loaded` : "Loading…"}
          </CardDescription>
        </CardHeader>
        <CardContent>
          {q.isLoading ? (
            <div className="space-y-2">
              <Skeleton className="h-10 w-full" />
              <Skeleton className="h-10 w-full" />
              <Skeleton className="h-10 w-full" />
            </div>
          ) : q.isError ? (
            <ErrorState
              message={isApiError(q.error) ? q.error.message : "Failed to load sessions"}
              onRetry={() => void q.refetch()}
            />
          ) : rows.length === 0 ? (
            <p className="py-8 text-center text-sm text-muted-foreground">
              No sessions across any org yet.
            </p>
          ) : (
            <div className="overflow-x-auto">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Session</TableHead>
                    <TableHead>Org</TableHead>
                    <TableHead>Status</TableHead>
                    <TableHead>Phone</TableHead>
                    <TableHead>Kind</TableHead>
                    <TableHead className="text-right">Last connected</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {rows.map((s) => (
                    <TableRow key={s.id}>
                      <TableCell>
                        <div className="font-medium">{s.label || s.id}</div>
                        <div className="font-mono text-xs text-muted-foreground">{s.id}</div>
                      </TableCell>
                      <TableCell className="font-mono text-xs text-muted-foreground">
                        {s.tenantId}
                      </TableCell>
                      <TableCell>
                        <SessionStatusBadge status={withLiveStatus(s, overrides)} />
                      </TableCell>
                      <TableCell className="text-sm">{s.phoneNumber || "—"}</TableCell>
                      <TableCell>
                        {s.isAdminSession ? (
                          <Badge variant="secondary">admin</Badge>
                        ) : (
                          <span className="text-sm text-muted-foreground">user</span>
                        )}
                      </TableCell>
                      <TableCell className="text-right text-sm text-muted-foreground">
                        {fmtTime(s.lastConnectedAt)}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          )}

          {q.hasNextPage && (
            <div className="mt-4 flex justify-center">
              <Button
                variant="outline"
                size="sm"
                disabled={q.isFetchingNextPage}
                onClick={() => void q.fetchNextPage()}
              >
                {q.isFetchingNextPage ? "Loading…" : "Load more"}
              </Button>
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

function ErrorState({ message, onRetry }: { message: string; onRetry: () => void }) {
  return (
    <div className="space-y-3 py-6 text-center">
      <p className="text-sm text-destructive">{message}</p>
      <Button variant="outline" size="sm" onClick={onRetry}>
        Retry
      </Button>
    </div>
  );
}
