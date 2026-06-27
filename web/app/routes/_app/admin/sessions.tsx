// Admin: all WhatsApp sessions across ALL orgs, with live statuses.
// Gateway GET /admin/sessions via useAdminSessions (TanStack Query infinite).
// Live status overlay rides the shared NDJSON firehose (no second socket).
//
// Ported from v1 admin/sessions.tsx: clientLoader=requireAdmin -> the parent
// /admin route's super_admin beforeLoad; imports repointed to ./-shared.
//
// NOTE (R5): WASession (app/lib/api/schema.d.ts) now exposes `organizationId`
// (owning org) and `gatewayId` (where the session lives). We render
// `organizationId` under the "Org" column; the masterplan §12 also allows
// surfacing `s.gatewayId` here once a multi-gateway registry is in play.

import { useMemo } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { DatabaseBackupIcon } from "lucide-react";
import {
  SessionStatusBadge,
  StreamIndicator,
  useLiveSessionStatus,
  withLiveStatus,
  fmtTime,
} from "./-shared";
import {
  useAdminSessionBackfill,
  useAdminSessions,
  useStartAdminSessionBackfill,
} from "~/lib/api/hooks/admin";
import { useEventStreamSubscription } from "~/lib/events/useEventStream";
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
import { toast } from "sonner";

export const Route = createFileRoute("/_app/admin/sessions")({
  component: AdminSessions,
});

function AdminSessions() {
  useEventStreamSubscription();
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
                    <TableHead className="text-right">Backfill</TableHead>
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
                        {s.organizationId}
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
                      <TableCell className="text-right">
                        <BackfillCell session={s} />
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

function BackfillCell({ session }: { session: WASession }) {
  const status = useAdminSessionBackfill(session.id);
  const start = useStartAdminSessionBackfill();
  const job = status.data;
  const running = job?.status === "running" || start.isPending;

  const onBackfill = () => {
    start.mutate(session.id, {
      onSuccess: () => toast.success("Backfill started"),
      onError: (err) =>
        toast.error(isApiError(err) ? err.message : "Backfill failed"),
    });
  };

  return (
    <div className="flex items-center justify-end gap-2">
      {job ? (
        <span className="text-xs text-muted-foreground">
          {job.status === "succeeded"
            ? `${job.contacts} contacts / ${job.groups} groups`
            : job.status}
        </span>
      ) : null}
      <Button
        variant="outline"
        size="sm"
        className="gap-1.5"
        disabled={running}
        onClick={onBackfill}
      >
        <DatabaseBackupIcon className="size-4" aria-hidden />
        {running ? "Running" : "Backfill"}
      </Button>
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
