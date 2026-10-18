// Admin: one gateway's health, lifecycle controls, assigned sessions,
// reconciliation results and audit trail. Only the lifecycle actions the API
// accepts in the current state are offered (see availableActions).

import { useState, type ReactNode } from "react";
import { Link, createFileRoute, useNavigate } from "@tanstack/react-router";
import {
  CheckCircle2Icon,
  CircleDashedIcon,
  KeyRoundIcon,
  PauseIcon,
  PlayIcon,
  PowerIcon,
  PowerOffIcon,
  RefreshCwIcon,
  RotateCcwKeyIcon,
  Trash2Icon,
  XCircleIcon,
} from "lucide-react";
import {
  useAdminGateway,
  useAdminGatewayAction,
  useAdminGatewayEnrollment,
  useDeleteAdminGateway,
} from "~/lib/api/hooks/admin";
import { isApiError } from "~/lib/api/envelope";
import type { components } from "~/lib/api/schema";
import { Button } from "~/components/ui/button";
import { Badge } from "~/components/ui/badge";
import { Skeleton } from "~/components/ui/skeleton";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "~/components/ui/card";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "~/components/ui/dialog";
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
import { SessionStatusBadge } from "~/components/status-badge";
import { Time } from "~/components/time";
import { titleHead } from "~/lib/head";
import { cn } from "~/lib/utils";
import { toast } from "sonner";
import { GatewayEnrollmentDialog, type Enrollment } from "./-gateway-enrollment";
import {
  CONNECTION_MODE_LABEL,
  LIFECYCLE_LABEL,
  RECONCILIATION_STATUS,
  RESULT_STATUS,
  availableActions,
  capabilityLabels,
  deleteChecks,
  gatewayStatusMeta,
  humanize,
  journalPressure,
  keystoreBytes,
  keystorePresence,
  revisionGap,
  type DeleteCheck,
} from "./-gateway-reconciliation";
import { OrgLabel, ToneBadge, useOrgNames } from "./-shared";

type GatewayDetailData = components["schemas"]["GatewayAdminDetail"];
type Gateway = components["schemas"]["GatewayAdmin"];

export const Route = createFileRoute("/_app/admin/gateways/$gatewayId")({
  head: () => titleHead("Gateway", "Gateways"),
  component: GatewayDetail,
});

const backLink = (
  <Link to="/admin/gateways" className="underline-offset-4 hover:text-foreground hover:underline">
    ← Gateways
  </Link>
);

function errorText(error: unknown, fallback: string): string {
  return isApiError(error) ? error.message : error instanceof Error ? error.message : fallback;
}

function GatewayDetail() {
  const { gatewayId } = Route.useParams();
  const query = useAdminGateway(gatewayId);
  const [result, setResult] = useState<Enrollment | null>(null);

  if (query.isLoading) {
    return (
      <Page>
        <PageTitle eyebrow={backLink} title={<Skeleton className="h-6 w-48" />} />
        <div className="grid gap-4 lg:grid-cols-3">
          <Skeleton className="h-56 rounded-xl" />
          <Skeleton className="h-56 rounded-xl" />
          <Skeleton className="h-56 rounded-xl" />
        </div>
        <Skeleton className="h-32 rounded-xl" />
      </Page>
    );
  }
  if (query.isError || !query.data) {
    return (
      <Page>
        <PageTitle
          eyebrow={backLink}
          title="Gateway not found"
          description={query.isError ? errorText(query.error, "Could not load this gateway.") : "It may have been deleted."}
          actions={
            <Button variant="outline" onClick={() => void query.refetch()}>
              <RefreshCwIcon aria-hidden />
              Retry
            </Button>
          }
        />
      </Page>
    );
  }

  const detail = query.data;
  const { gateway } = detail;
  const name = gateway.label || "Unnamed gateway";

  return (
    <Page>
      <PageTitle
        eyebrow={backLink}
        title={
          <>
            <span className="truncate">{name}</span>
            <ToneBadge meta={gatewayStatusMeta(gateway.status)} />
          </>
        }
        description={
          <span className="flex flex-wrap items-center gap-x-3 gap-y-1">
            <CopyableId value={gateway.id} label="Copy gateway ID" />
            {gateway.notes ? <span className="text-pretty">{gateway.notes}</span> : null}
          </span>
        }
        actions={
          <Button variant="outline" disabled={query.isFetching} onClick={() => void query.refetch()}>
            <RefreshCwIcon className={query.isFetching ? "animate-spin" : undefined} aria-hidden />
            Refresh
          </Button>
        }
      />

      <div className="grid gap-4 lg:grid-cols-3">
        <FactCard title="Health" description="Connection and placement.">
          <Fact label="Reconciliation">
            <ToneBadge meta={RECONCILIATION_STATUS[gateway.reconciliationStatus] ?? gatewayStatusMeta(gateway.reconciliationStatus)} />
          </Fact>
          <Fact label="Placement">{LIFECYCLE_LABEL[gateway.desiredLifecycle] ?? humanize(gateway.desiredLifecycle)}</Fact>
          <Fact label="Sessions">
            <span className="tabular-nums">
              {gateway.sessionCount} live · {detail.assignedSessionCount} assigned
              {gateway.capacity ? ` · limit ${gateway.capacity}` : ""}
            </span>
          </Fact>
          <Fact label="Last heartbeat"><Time value={gateway.lastSeenAt} fallback="Never" /></Fact>
          <Fact label="Connected since"><Time value={gateway.connectedAt} fallback="Not connected" /></Fact>
        </FactCard>

        <FactCard title="Keystore" description="WhatsApp keys and the event journal on the gateway's disk.">
          <Fact label="Keystore">
            {keystorePresence(gateway)}
            {gateway.keystoreIntegrity ? ` · ${humanize(gateway.keystoreIntegrity)}` : ""}
          </Fact>
          <Fact label="Size">{keystoreBytes(gateway)}</Fact>
          <Fact label="Last checked"><Time value={gateway.keystoreCheckedAt} fallback="Not reported" /></Fact>
          <Fact label="Event journal">{journalPressure(gateway)}</Fact>
        </FactCard>

        <FactCard title="Version" description="Software, configuration and credentials.">
          <Fact label="Software"><span className="font-mono text-xs">{gateway.softwareVersion || "Not reported"}</span></Fact>
          <Fact label="Config revision">
            {gateway.appliedRevision === gateway.desiredRevision
              ? `${gateway.appliedRevision} (in sync)`
              : revisionGap(gateway.appliedRevision, gateway.desiredRevision)}
          </Fact>
          <Fact label="Connection">
            {CONNECTION_MODE_LABEL[gateway.connectionMode] ?? humanize(gateway.connectionMode)}
            <span className="text-muted-foreground"> · epoch {gateway.connectionEpoch}</span>
          </Fact>
          <Fact label="Certificate expires">
            {detail.activeCertificate ? <Time value={detail.activeCertificate.notAfter} /> : "No active certificate"}
          </Fact>
          <Fact label="Capabilities"><Capabilities value={gateway.capabilities} /></Fact>
        </FactCard>
      </div>

      <LifecycleCard detail={detail} name={name} onEnrollment={setResult} />

      <section className="grid gap-3">
        <SectionHeading title="Assigned sessions" count={detail.assignedSessionCount} />
        <AssignedSessions sessions={detail.assignedSessions ?? []} />
      </section>

      <section className="grid gap-3">
        <SectionHeading
          title="Reconciliation results"
          description="How each local device matches the sessions assigned to this gateway."
        />
        <ReconciliationTable results={detail.reconciliationResults ?? []} />
      </section>

      <section className="grid gap-3">
        <SectionHeading title="Activity" description="Recent lifecycle and enrollment events." />
        <AuditTable entries={detail.audit ?? []} gateway={gateway} />
      </section>

      <GatewayEnrollmentDialog
        result={result}
        title="New enrollment token"
        onClose={() => setResult(null)}
      />
    </Page>
  );
}

// --- Facts -------------------------------------------------------------------

function FactCard({ title, description, children }: { title: string; description: string; children: ReactNode }) {
  return (
    <Card className="gap-4">
      <CardHeader>
        <CardTitle className="text-base">{title}</CardTitle>
        <CardDescription>{description}</CardDescription>
      </CardHeader>
      <CardContent>
        <dl className="grid gap-3 text-sm">{children}</dl>
      </CardContent>
    </Card>
  );
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid grid-cols-[7.5rem_1fr] items-baseline gap-3">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0 break-words">{children}</dd>
    </div>
  );
}

function Capabilities({ value }: { value: unknown }) {
  const labels = capabilityLabels(value);
  if (labels.length === 0) return <span className="text-muted-foreground">None reported</span>;
  return (
    <span className="flex flex-wrap gap-1">
      {labels.map((label) => (
        <Badge key={label} variant="secondary">{label}</Badge>
      ))}
    </span>
  );
}

function SectionHeading({ title, description, count }: { title: string; description?: string; count?: number }) {
  return (
    <div className="flex flex-col gap-0.5">
      <h2 className="flex items-center gap-2 text-base font-semibold tracking-tight">
        {title}
        {count !== undefined ? (
          <span className="rounded-full bg-muted px-2 py-0.5 text-xs font-medium text-muted-foreground tabular-nums">{count}</span>
        ) : null}
      </h2>
      {description ? <p className="text-sm text-muted-foreground">{description}</p> : null}
    </div>
  );
}

// --- Lifecycle ----------------------------------------------------------------

function LifecycleCard({
  detail,
  name,
  onEnrollment,
}: {
  detail: GatewayDetailData;
  name: string;
  onEnrollment: (result: Enrollment) => void;
}) {
  const { gateway } = detail;
  const confirm = useConfirm();
  const action = useAdminGatewayAction();
  const reenroll = useAdminGatewayEnrollment("reenroll");
  const replace = useAdminGatewayEnrollment("replace-enrollment-token");
  const [deleteOpen, setDeleteOpen] = useState(false);
  const busy = action.isPending || reenroll.isPending || replace.isPending;
  const actions = availableActions(gateway);
  const sessions = detail.assignedSessionCount;
  const sessionWord = `${sessions} assigned session${sessions === 1 ? "" : "s"}`;

  const run = (
    name: "disable" | "drain" | "reenable" | "resume",
    success: string,
  ) =>
    action.mutate(
      { gatewayId: gateway.id, action: name },
      {
        onSuccess: () => toast.success(success),
        onError: (err) => toast.error(errorText(err, "Gateway action failed")),
      },
    );

  const disable = async () => {
    const ok = await confirm({
      title: <>Disable “{name}”?</>,
      description: `The API drops its connection now and rejects reconnects. Its ${sessionWord} go offline until you re-enable it.`,
      confirmLabel: "Disable gateway",
      destructive: true,
    });
    if (ok) run("disable", "Gateway disabled");
  };

  const doReenroll = async () => {
    const ok = await confirm({
      title: <>Re-enroll “{name}”?</>,
      description:
        "This revokes its certificate and any unused token, then issues a new enrollment token. The gateway can't reconnect until it enrolls again with the new token.",
      confirmLabel: "Re-enroll",
      destructive: true,
    });
    if (!ok) return;
    reenroll.mutate(gateway.id, {
      onSuccess: onEnrollment,
      onError: (err) => toast.error(errorText(err, "Re-enroll failed")),
    });
  };

  const doReplace = async () => {
    const ok = await confirm({
      title: "Replace the enrollment token?",
      description: "The current token stops working immediately. The new one is shown once.",
      confirmLabel: "Replace token",
    });
    if (!ok) return;
    replace.mutate(gateway.id, {
      onSuccess: onEnrollment,
      onError: (err) => toast.error(errorText(err, "Could not replace the token")),
    });
  };

  const rows: Array<{ key: string; show: boolean; icon: ReactNode; title: string; text: string; button: ReactNode }> = [
    {
      key: "drain",
      show: actions.has("drain"),
      icon: <PauseIcon aria-hidden />,
      title: "Drain",
      text: "Stop placing new sessions here. Current sessions keep running.",
      button: (
        <Button variant="outline" disabled={busy} onClick={() => run("drain", "Draining: no new sessions will be placed here")}>
          Drain
        </Button>
      ),
    },
    {
      key: "resume",
      show: actions.has("resume"),
      icon: <PlayIcon aria-hidden />,
      title: "Resume",
      text: "Accept new sessions again.",
      button: (
        <Button variant="outline" disabled={busy} onClick={() => run("resume", "Gateway resumed")}>
          Resume
        </Button>
      ),
    },
    {
      key: "reenable",
      show: actions.has("reenable"),
      icon: <PowerIcon aria-hidden />,
      title: "Re-enable",
      text: "Let the gateway connect again.",
      button: (
        <Button variant="outline" disabled={busy} onClick={() => run("reenable", "Gateway re-enabled")}>
          Re-enable
        </Button>
      ),
    },
    {
      key: "replaceToken",
      show: actions.has("replaceToken"),
      icon: <KeyRoundIcon aria-hidden />,
      title: "Replace enrollment token",
      text: "Lost the token, or it expired? Issue a new one.",
      button: (
        <Button variant="outline" disabled={busy} onClick={() => void doReplace()}>
          {replace.isPending ? "Replacing…" : "Replace token"}
        </Button>
      ),
    },
    {
      key: "reenroll",
      show: actions.has("reenroll"),
      icon: <RotateCcwKeyIcon aria-hidden />,
      title: "Re-enroll",
      text: "Revoke its credentials and issue a new enrollment token, e.g. after moving it to a new host.",
      button: (
        <Button variant="outline" disabled={busy} onClick={() => void doReenroll()}>
          {reenroll.isPending ? "Re-enrolling…" : "Re-enroll"}
        </Button>
      ),
    },
    {
      key: "disable",
      show: actions.has("disable"),
      icon: <PowerOffIcon aria-hidden />,
      title: "Disable",
      text: "Reject the gateway's connection immediately. Its sessions go offline.",
      button: (
        <Button variant="destructive" disabled={busy} onClick={() => void disable()}>
          Disable
        </Button>
      ),
    },
    {
      key: "delete",
      show: true,
      icon: <Trash2Icon aria-hidden />,
      title: "Delete",
      text: "Remove the gateway from the registry. Only possible once it's drained or disabled and empty.",
      button: (
        <Button variant="destructive" disabled={busy} onClick={() => setDeleteOpen(true)}>
          Delete…
        </Button>
      ),
    },
  ];

  return (
    <Card className="gap-0 py-0">
      <CardHeader className="py-4">
        <CardTitle className="text-base">Lifecycle</CardTitle>
        <CardDescription>
          {gatewayStatusMeta(gateway.status).description || "Control whether this gateway takes sessions."}
        </CardDescription>
      </CardHeader>
      <ul className="divide-y border-t">
        {rows
          .filter((row) => row.show)
          .map((row) => (
            <li key={row.key} className="flex flex-col gap-3 px-6 py-3.5 sm:flex-row sm:items-center sm:justify-between">
              <div className="flex min-w-0 items-start gap-3">
                <span className="mt-0.5 text-muted-foreground [&_svg]:size-4">{row.icon}</span>
                <div className="min-w-0">
                  <div className="text-sm font-medium">{row.title}</div>
                  <div className="text-sm text-muted-foreground">{row.text}</div>
                </div>
              </div>
              <div className="shrink-0 pl-7 sm:pl-0">{row.button}</div>
            </li>
          ))}
      </ul>
      <DeleteGatewayDialog open={deleteOpen} onOpenChange={setDeleteOpen} detail={detail} name={name} />
    </Card>
  );
}

function DeleteGatewayDialog({
  open,
  onOpenChange,
  detail,
  name,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  detail: GatewayDetailData;
  name: string;
}) {
  const navigate = useNavigate();
  const del = useDeleteAdminGateway();
  const checks = deleteChecks(detail);
  const blocked = checks.some((c) => c.ok === false);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Delete “{name}”?</DialogTitle>
          <DialogDescription>
            The gateway is removed from the registry. This can't be undone.
          </DialogDescription>
        </DialogHeader>
        <ul className="grid gap-2 rounded-xl border bg-muted/30 p-3">
          {checks.map((check) => (
            <CheckRow key={check.label} check={check} />
          ))}
        </ul>
        {del.isError && <p className="text-sm text-destructive">{errorText(del.error, "Could not delete the gateway")}</p>}
        <DialogFooter>
          <DialogClose asChild>
            <Button variant="outline">Cancel</Button>
          </DialogClose>
          <Button
            variant="destructive"
            disabled={blocked || del.isPending}
            onClick={() =>
              del.mutate(detail.gateway.id, {
                onSuccess: () => {
                  toast.success("Gateway deleted");
                  void navigate({ to: "/admin/gateways" });
                },
              })
            }
          >
            {del.isPending ? "Deleting…" : "Delete gateway"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function CheckRow({ check }: { check: DeleteCheck }) {
  const Icon = check.ok === true ? CheckCircle2Icon : check.ok === false ? XCircleIcon : CircleDashedIcon;
  return (
    <li className="flex items-start gap-2 text-sm">
      <Icon
        aria-hidden
        className={cn(
          "mt-0.5 size-4 shrink-0",
          check.ok === true && "text-emerald-600 dark:text-emerald-400",
          check.ok === false && "text-destructive",
          check.ok === null && "text-muted-foreground",
        )}
      />
      <div>
        <div className={cn(check.ok === false && "font-medium")}>{check.label}</div>
        {check.hint ? <div className="text-xs text-muted-foreground">{check.hint}</div> : null}
      </div>
    </li>
  );
}

// --- Tables -------------------------------------------------------------------

function EmptyRow({ children }: { children: ReactNode }) {
  return <p className="px-4 py-8 text-center text-sm text-muted-foreground">{children}</p>;
}

function AssignedSessions({ sessions }: { sessions: components["schemas"]["WASession"][] }) {
  const orgNames = useOrgNames();
  return (
    <TableCard>
      {sessions.length === 0 ? (
        <EmptyRow>No sessions are assigned to this gateway.</EmptyRow>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Session</TableHead>
              <TableHead>Status</TableHead>
              <TableHead>Organization</TableHead>
              <TableHead>Phone</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {sessions.map((session) => (
              <TableRow key={session.id}>
                <TableCell>
                  <div className="font-medium">{session.label || "Untitled session"}</div>
                  <div className="text-muted-foreground">
                    <CopyableId value={session.id} truncate label="Copy session ID" />
                  </div>
                </TableCell>
                <TableCell><SessionStatusBadge status={session.status} /></TableCell>
                <TableCell>
                  {session.organizationId ? (
                    <Link
                      to="/admin/sessions"
                      search={{ org: session.organizationId }}
                      className="underline-offset-4 hover:underline"
                    >
                      <OrgLabel id={session.organizationId} names={orgNames} />
                    </Link>
                  ) : (
                    <OrgLabel id={undefined} names={orgNames} />
                  )}
                </TableCell>
                <TableCell className="tabular-nums">{session.phoneNumber ? `+${session.phoneNumber}` : "—"}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </TableCard>
  );
}

function ReconciliationTable({ results }: { results: components["schemas"]["GatewayReconciliationResult"][] }) {
  return (
    <TableCard>
      {results.length === 0 ? (
        <EmptyRow>The gateway hasn't reported any devices yet.</EmptyRow>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Device</TableHead>
              <TableHead>Session</TableHead>
              <TableHead>Result</TableHead>
              <TableHead>Revision</TableHead>
              <TableHead>Updated</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {results.map((entry) => (
              <TableRow key={`${entry.deviceJid}:${entry.assignmentEpoch}`}>
                <TableCell className="text-muted-foreground">
                  <CopyableId value={entry.deviceJid} label="Copy device JID" />
                </TableCell>
                <TableCell>
                  {entry.status === "unexpected_local_device" ? (
                    <span className="text-muted-foreground">Unassigned local device</span>
                  ) : entry.sessionId ? (
                    <CopyableId value={entry.sessionId} truncate label="Copy session ID" />
                  ) : (
                    <span className="text-muted-foreground">No assigned session</span>
                  )}
                </TableCell>
                <TableCell>
                  <ToneBadge meta={RESULT_STATUS[entry.status] ?? gatewayStatusMeta(entry.status)} />
                </TableCell>
                <TableCell className="text-muted-foreground tabular-nums">
                  rev {entry.desiredRevision} · epoch {entry.assignmentEpoch}
                </TableCell>
                <TableCell className="text-muted-foreground"><Time value={entry.updatedAt} /></TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </TableCard>
  );
}

function actorLabel(entry: components["schemas"]["GatewayAuditEntry"], gateway: Gateway): ReactNode {
  const type = entry.actorType as string;
  if (type === "gateway" || entry.actorId === gateway.id) return "This gateway";
  if (type === "system") return "System";
  return entry.actorId ? (
    <span className="inline-flex items-center gap-1">
      Admin <CopyableId value={entry.actorId} truncate={[6, 4]} label="Copy user ID" />
    </span>
  ) : (
    "Admin"
  );
}

function AuditTable({ entries, gateway }: { entries: components["schemas"]["GatewayAuditEntry"][]; gateway: Gateway }) {
  return (
    <TableCard>
      {entries.length === 0 ? (
        <EmptyRow>No activity recorded yet.</EmptyRow>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>When</TableHead>
              <TableHead>Event</TableHead>
              <TableHead>By</TableHead>
              <TableHead>Outcome</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {entries.map((entry) => (
              <TableRow key={entry.id}>
                <TableCell className="text-muted-foreground"><Time value={entry.createdAt} /></TableCell>
                <TableCell className="font-medium">{humanize(entry.action)}</TableCell>
                <TableCell className="text-muted-foreground">{actorLabel(entry, gateway)}</TableCell>
                <TableCell>
                  <ToneBadge
                    meta={{
                      label: humanize(entry.outcome),
                      description: "",
                      tone: entry.outcome === "success" ? "ok" : entry.outcome === "failure" || entry.outcome === "denied" ? "bad" : "muted",
                    }}
                  />
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </TableCard>
  );
}
