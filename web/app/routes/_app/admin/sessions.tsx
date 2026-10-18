// Admin: all WhatsApp sessions across ALL orgs, with live statuses.
// GET /admin/sessions via useAdminSessions (TanStack Query infinite). The live
// status overlay rides the shared event stream (no second socket). Filters are
// client-side over the loaded pages; `?org=` comes from links on other admin
// pages ("View sessions" on Users & orgs, org links on a gateway).

import { useMemo, useState } from "react";
import { Link, createFileRoute, useNavigate } from "@tanstack/react-router";
import { DatabaseBackupIcon, LayersIcon, RefreshCwIcon, SearchIcon, XIcon } from "lucide-react";
import { StreamIndicator, useLiveSessionStatus, withLiveStatus, OrgLabel, useOrgNames } from "./-shared";
import { useOrgs } from "./-admin-client";
import {
  useAdminSessionBackfill,
  useAdminSessions,
  useStartAdminSessionBackfill,
} from "~/lib/api/hooks/admin";
import { useEventStreamSubscription } from "~/lib/events/useEventStream";
import type { SessionStatus, WASession } from "~/lib/api/types";
import { isApiError } from "~/lib/api/envelope";
import { Button } from "~/components/ui/button";
import { Badge } from "~/components/ui/badge";
import { Input } from "~/components/ui/input";
import { Skeleton } from "~/components/ui/skeleton";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "~/components/ui/select";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "~/components/ui/tooltip";
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
import { Page, PageTitle } from "~/components/shell/page-title";
import { useConfirm } from "~/components/confirm-dialog";
import { CopyableId } from "~/components/copyable";
import { SESSION_STATUS, SessionStatusBadge } from "~/components/status-badge";
import { Time } from "~/components/time";
import { formatRelative, middleTruncate } from "~/lib/format";
import { titleHead } from "~/lib/head";
import { toast } from "sonner";

export const Route = createFileRoute("/_app/admin/sessions")({
  validateSearch: (search: Record<string, unknown>): { org?: string } => ({
    org: typeof search.org === "string" && search.org ? search.org : undefined,
  }),
  head: () => titleHead("All sessions"),
  component: AdminSessions,
});

const STATUS_ORDER: SessionStatus[] = ["working", "scan_qr_code", "starting", "stopped", "logged_out", "failed"];

function AdminSessions() {
  useEventStreamSubscription();
  const q = useAdminSessions();
  const overrides = useLiveSessionStatus();
  const orgNames = useOrgNames();
  const orgs = useOrgs();
  const { org } = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const [search, setSearch] = useState("");
  const [status, setStatus] = useState<"all" | SessionStatus>("all");

  const rows: WASession[] = useMemo(
    () => q.data?.pages.flatMap((p) => p.data ?? []) ?? [],
    [q.data],
  );

  const filtered = useMemo(() => {
    const needle = search.trim().toLowerCase();
    return rows.filter((s) => {
      if (org && s.organizationId !== org) return false;
      if (status !== "all" && withLiveStatus(s, overrides) !== status) return false;
      if (!needle) return true;
      return [s.label, s.id, s.phoneNumber, s.organizationId && orgNames.get(s.organizationId), s.gatewayId]
        .filter((v): v is string => typeof v === "string")
        .some((v) => v.toLowerCase().includes(needle));
    });
  }, [rows, org, status, search, overrides, orgNames]);

  const setOrg = (next: string | undefined) =>
    void navigate({ search: (prev) => ({ ...prev, org: next }), replace: true });
  const filtering = Boolean(search.trim() || status !== "all" || org);

  return (
    <Page>
      <PageTitle
        title="All sessions"
        description="Every WhatsApp session across all organizations, with its live status."
        actions={<StreamIndicator />}
      />

      <div className="flex flex-col gap-2 sm:flex-row sm:flex-wrap sm:items-center">
        <div className="relative w-full sm:w-72">
          <SearchIcon className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" aria-hidden />
          <Input
            type="search"
            placeholder="Search name, phone, ID…"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            className="pl-8"
            aria-label="Search sessions"
          />
        </div>
        <Select value={status} onValueChange={(v) => setStatus(v as typeof status)}>
          <SelectTrigger className="w-full sm:w-48" aria-label="Filter by status">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All statuses</SelectItem>
            {STATUS_ORDER.map((s) => (
              <SelectItem key={s} value={s}>{SESSION_STATUS[s].label}</SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select value={org ?? "all"} onValueChange={(v) => setOrg(v === "all" ? undefined : v)}>
          <SelectTrigger className="w-full sm:w-56" aria-label="Filter by organization">
            <SelectValue placeholder="All organizations" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All organizations</SelectItem>
            {org && !orgs.data?.some((o) => o.id === org) ? (
              <SelectItem value={org}>{middleTruncate(org)}</SelectItem>
            ) : null}
            {(orgs.data ?? []).map((o) => (
              <SelectItem key={o.id} value={o.id}>{o.name}</SelectItem>
            ))}
          </SelectContent>
        </Select>
        {filtering ? (
          <Button
            variant="ghost"
            onClick={() => {
              setSearch("");
              setStatus("all");
              setOrg(undefined);
            }}
          >
            <XIcon aria-hidden />
            Clear filters
          </Button>
        ) : null}
        <span className="text-sm text-muted-foreground sm:ml-auto">
          {q.isSuccess
            ? filtering
              ? `${filtered.length} of ${rows.length}${q.hasNextPage ? " loaded" : ""} sessions`
              : `${rows.length}${q.hasNextPage ? "+" : ""} session${rows.length === 1 ? "" : "s"}`
            : null}
        </span>
      </div>

      <TableCard>
        {q.isLoading ? (
          <div className="space-y-2 p-4">
            <Skeleton className="h-12 w-full" />
            <Skeleton className="h-12 w-full" />
            <Skeleton className="h-12 w-full" />
          </div>
        ) : q.isError ? (
          <div className="flex flex-col items-center gap-3 py-10 text-center">
            <p className="text-sm text-destructive">
              {isApiError(q.error) ? q.error.message : "Failed to load sessions"}
            </p>
            <Button variant="outline" size="sm" onClick={() => void q.refetch()}>
              <RefreshCwIcon aria-hidden />
              Retry
            </Button>
          </div>
        ) : filtered.length === 0 ? (
          <Empty className="border-0">
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <LayersIcon aria-hidden />
              </EmptyMedia>
              <EmptyTitle>{rows.length === 0 ? "No sessions yet" : "No sessions match"}</EmptyTitle>
              <EmptyDescription>
                {rows.length === 0
                  ? "Sessions appear here when anyone in any organization connects a WhatsApp number."
                  : q.hasNextPage
                    ? "Nothing in the loaded sessions matches. Load more or clear the filters."
                    : "Try a different search or clear the filters."}
              </EmptyDescription>
            </EmptyHeader>
          </Empty>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Session</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>Organization</TableHead>
                <TableHead>Phone</TableHead>
                <TableHead>Gateway</TableHead>
                <TableHead>Last connected</TableHead>
                <TableHead className="text-right">Contacts & groups</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {filtered.map((s) => (
                <TableRow key={s.id}>
                  <TableCell>
                    <div className="flex items-center gap-2 font-medium">
                      {s.label || "Untitled session"}
                      {s.isAdminSession ? <Badge variant="secondary">Admin number</Badge> : null}
                    </div>
                    <div className="text-muted-foreground">
                      <CopyableId value={s.id} truncate label="Copy session ID" />
                    </div>
                  </TableCell>
                  <TableCell>
                    <SessionStatusBadge status={withLiveStatus(s, overrides)} />
                  </TableCell>
                  <TableCell className="max-w-48">
                    {s.organizationId ? (
                      <button
                        type="button"
                        className="max-w-full truncate text-left underline-offset-4 hover:underline"
                        title="Show only this organization"
                        onClick={() => setOrg(s.organizationId)}
                      >
                        <OrgLabel id={s.organizationId} names={orgNames} />
                      </button>
                    ) : (
                      <OrgLabel id={undefined} names={orgNames} />
                    )}
                  </TableCell>
                  <TableCell className="tabular-nums">
                    {s.phoneNumber ? `+${s.phoneNumber}` : <span className="text-muted-foreground">Not paired</span>}
                  </TableCell>
                  <TableCell>
                    {s.gatewayId ? (
                      <Link
                        to="/admin/gateways/$gatewayId"
                        params={{ gatewayId: s.gatewayId }}
                        title={s.gatewayId}
                        className="font-mono text-xs text-muted-foreground underline-offset-4 hover:text-foreground hover:underline"
                      >
                        {middleTruncate(s.gatewayId, 6, 4)}
                      </Link>
                    ) : (
                      <span className="text-muted-foreground">—</span>
                    )}
                  </TableCell>
                  <TableCell className="text-muted-foreground">
                    <Time value={s.lastConnectedAt} fallback="—" />
                  </TableCell>
                  <TableCell className="text-right">
                    <BackfillCell session={s} />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </TableCard>

      {q.hasNextPage && (
        <div className="flex justify-center">
          <Button variant="outline" disabled={q.isFetchingNextPage} onClick={() => void q.fetchNextPage()}>
            {q.isFetchingNextPage ? "Loading…" : "Load more"}
          </Button>
        </div>
      )}
    </Page>
  );
}

function plural(n: number, word: string) {
  return `${n} ${word}${n === 1 ? "" : "s"}`;
}

function BackfillCell({ session }: { session: WASession }) {
  const status = useAdminSessionBackfill(session.id);
  const start = useStartAdminSessionBackfill();
  const confirm = useConfirm();
  const job = status.data;
  const running = job?.status === "running" || start.isPending;

  const onBackfill = async () => {
    const ok = await confirm({
      title: <>Re-sync “{session.label || "this session"}”?</>,
      description:
        "Fetches the contact list and every group (with members) from WhatsApp and saves them. It runs in the background and can take a minute on large accounts.",
      confirmLabel: "Re-sync",
    });
    if (!ok) return;
    start.mutate(session.id, {
      onSuccess: () => toast.success("Re-sync started"),
      onError: (err) => toast.error(isApiError(err) ? err.message : "Re-sync failed"),
    });
  };

  const summary = !job
    ? null
    : job.status === "running"
      ? <span className="text-muted-foreground">Syncing… {plural(job.contacts, "contact")}</span>
      : job.status === "succeeded"
        ? (
            <span className="text-muted-foreground" title={job.finishedAt ? `Finished ${formatRelative(job.finishedAt)}` : undefined}>
              {plural(job.contacts, "contact")} · {plural(job.groups, "group")}
            </span>
          )
        : <span className="text-destructive" title={job.error}>Failed{job.error ? `: ${job.error}` : ""}</span>;

  return (
    <div className="flex items-center justify-end gap-2 text-xs">
      {summary}
      <TooltipProvider delayDuration={300}>
        <Tooltip>
          <TooltipTrigger asChild>
            <span className="inline-flex">
              <Button variant="outline" size="sm" disabled={running} onClick={() => void onBackfill()}>
                <DatabaseBackupIcon aria-hidden />
                {running ? "Syncing…" : "Re-sync"}
              </Button>
            </span>
          </TooltipTrigger>
          <TooltipContent>Re-sync contacts and groups from WhatsApp</TooltipContent>
        </Tooltip>
      </TooltipProvider>
    </div>
  );
}
