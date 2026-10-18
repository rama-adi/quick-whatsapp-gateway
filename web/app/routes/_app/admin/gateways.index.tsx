// Admin: gateway inventory. Each row is a gateway process the API places
// sessions on; "Add gateway" registers one and reveals its single-use
// enrollment token once (held only in component state, never cached).

import { useState } from "react";
import { Link, createFileRoute } from "@tanstack/react-router";
import { PlusIcon, RefreshCwIcon, ServerIcon } from "lucide-react";
import { useAdminGateways, useCreateAdminGateway } from "~/lib/api/hooks/admin";
import { isApiError } from "~/lib/api/envelope";
import { Button } from "~/components/ui/button";
import { Input } from "~/components/ui/input";
import { Label } from "~/components/ui/label";
import { Textarea } from "~/components/ui/textarea";
import { Skeleton } from "~/components/ui/skeleton";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "~/components/ui/dialog";
import {
  Empty,
  EmptyContent,
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
import { CopyableId } from "~/components/copyable";
import { Time } from "~/components/time";
import { titleHead } from "~/lib/head";
import { toast } from "sonner";
import { GatewayEnrollmentDialog, type Enrollment } from "./-gateway-enrollment";
import { gatewayStatusMeta } from "./-gateway-reconciliation";
import { ToneBadge } from "./-shared";

export const Route = createFileRoute("/_app/admin/gateways/")({
  head: () => titleHead("Gateways"),
  component: Gateways,
});

function Gateways() {
  const gateways = useAdminGateways();
  const [result, setResult] = useState<Enrollment | null>(null);
  const rows = gateways.data ?? [];

  return (
    <Page>
      <PageTitle
        title="Gateways"
        description="The processes that hold live WhatsApp connections. New sessions are placed on active gateways."
        actions={
          <>
            <Button
              variant="outline"
              size="icon"
              aria-label="Refresh"
              title="Refresh"
              disabled={gateways.isFetching}
              onClick={() => void gateways.refetch()}
            >
              <RefreshCwIcon className={gateways.isFetching ? "animate-spin" : undefined} aria-hidden />
            </Button>
            <AddGatewayDialog onCreated={setResult} />
          </>
        }
      />

      <TableCard>
        {gateways.isLoading ? (
          <div className="space-y-2 p-4">
            <Skeleton className="h-12 w-full" />
            <Skeleton className="h-12 w-full" />
          </div>
        ) : gateways.isError ? (
          <div className="flex flex-col items-center gap-3 py-10 text-center">
            <p className="text-sm text-destructive">
              {isApiError(gateways.error) ? gateways.error.message : "Could not load gateways"}
            </p>
            <Button variant="outline" size="sm" onClick={() => void gateways.refetch()}>
              <RefreshCwIcon aria-hidden />
              Retry
            </Button>
          </div>
        ) : rows.length === 0 ? (
          <Empty className="border-0">
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <ServerIcon aria-hidden />
              </EmptyMedia>
              <EmptyTitle>No gateways yet</EmptyTitle>
              <EmptyDescription>
                Add one to start placing sessions. You'll get a one-time token to
                enroll the gateway process.
              </EmptyDescription>
            </EmptyHeader>
            <EmptyContent>
              <AddGatewayDialog onCreated={setResult} />
            </EmptyContent>
          </Empty>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Gateway</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>Sessions</TableHead>
                <TableHead>Last heartbeat</TableHead>
                <TableHead>Version</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((gateway) => (
                <TableRow key={gateway.id}>
                  <TableCell>
                    <Link
                      to="/admin/gateways/$gatewayId"
                      params={{ gatewayId: gateway.id }}
                      className="font-medium underline-offset-4 hover:underline"
                    >
                      {gateway.label || "Unnamed gateway"}
                    </Link>
                    <div className="text-muted-foreground">
                      <CopyableId value={gateway.id} truncate label="Copy gateway ID" />
                    </div>
                  </TableCell>
                  <TableCell>
                    <ToneBadge meta={gatewayStatusMeta(gateway.status)} />
                  </TableCell>
                  <TableCell className="tabular-nums">
                    {gateway.sessionCount}
                    <span className="text-muted-foreground">
                      {gateway.capacity ? ` / ${gateway.capacity}` : ""}
                    </span>
                  </TableCell>
                  <TableCell className="text-muted-foreground">
                    <Time value={gateway.lastSeenAt} fallback="Never" />
                  </TableCell>
                  <TableCell className="font-mono text-xs text-muted-foreground">
                    {gateway.softwareVersion || "—"}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </TableCard>

      <GatewayEnrollmentDialog result={result} onClose={() => setResult(null)} />
    </Page>
  );
}

function AddGatewayDialog({ onCreated }: { onCreated: (result: Enrollment) => void }) {
  const create = useCreateAdminGateway();
  const [open, setOpen] = useState(false);
  const [label, setLabel] = useState("");
  const [capacity, setCapacity] = useState("");
  const [notes, setNotes] = useState("");

  const reset = () => {
    setLabel("");
    setCapacity("");
    setNotes("");
    create.reset();
  };

  const submit = (event: React.FormEvent) => {
    event.preventDefault();
    create.mutate(
      {
        label: label.trim() || undefined,
        notes: notes.trim() || undefined,
        capacity: capacity ? Number(capacity) : undefined,
      },
      {
        onSuccess: (result) => {
          setOpen(false);
          reset();
          onCreated(result);
          toast.success("Gateway added");
        },
      },
    );
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) reset();
      }}
    >
      <DialogTrigger asChild>
        <Button>
          <PlusIcon aria-hidden data-icon="inline-start" />
          Add gateway
        </Button>
      </DialogTrigger>
      <DialogContent>
        <form onSubmit={submit} className="grid gap-4">
          <DialogHeader>
            <DialogTitle>Add gateway</DialogTitle>
            <DialogDescription>
              Registers a gateway and issues a one-time enrollment token for it.
              All fields are optional.
            </DialogDescription>
          </DialogHeader>

          <div className="grid gap-4">
            <div className="grid gap-2">
              <Label htmlFor="gateway-label">Display name</Label>
              <Input
                id="gateway-label"
                value={label}
                placeholder="e.g. Singapore primary"
                onChange={(e) => setLabel(e.target.value)}
                autoFocus
              />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="gateway-capacity">Session limit</Label>
              <Input
                id="gateway-capacity"
                type="number"
                min="1"
                inputMode="numeric"
                placeholder="Unlimited"
                value={capacity}
                onChange={(e) => setCapacity(e.target.value)}
              />
              <p className="text-xs text-muted-foreground">
                The most sessions this gateway should host. Leave blank for no limit.
              </p>
            </div>
            <div className="grid gap-2">
              <Label htmlFor="gateway-notes">Notes</Label>
              <Textarea
                id="gateway-notes"
                value={notes}
                placeholder="Where it runs, who owns it…"
                onChange={(e) => setNotes(e.target.value)}
              />
              <p className="text-xs text-muted-foreground">
                Only visible to platform admins. Never sent to the gateway.
              </p>
            </div>
            {create.isError && (
              <p className="text-sm text-destructive">
                {isApiError(create.error) ? create.error.message : "Could not create gateway"}
              </p>
            )}
          </div>

          <DialogFooter>
            <DialogClose asChild>
              <Button type="button" variant="outline">
                Cancel
              </Button>
            </DialogClose>
            <Button type="submit" disabled={create.isPending}>
              {create.isPending ? "Adding…" : "Add gateway"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
