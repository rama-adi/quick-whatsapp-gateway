// User: webhooks CRUD — list, create, edit (PATCH), delete. §12 user surface,
// scoped to the active org (the gateway filters by the JWT's activeOrganizationId).
//
// Ported from v1 user/webhooks.tsx (tag mvp-v1). Reshape notes:
//   - Route object -> TanStack file-based route (/user/webhooks); the v1
//     `clientLoader = requireUserPanel` guard moved to the _app/user.tsx layout
//     beforeLoad (§12).
//   - Data + actions reuse the FROZEN gateway webhook hooks (browser -> gateway,
//     Bearer JWT): useWebhooks / useCreateWebhook / useUpdateWebhook /
//     useDeleteWebhook. Webhooks are the gateway's own config resource (§13), so
//     they stay on the gateway API (unlike API keys, which moved to better-auth).
//
// The HMAC secret + custom headers are write-only and never returned, so the
// edit form leaves them blank and only sends them when the user re-enters one.

import { useState } from "react";
import {
  PlusIcon,
  RefreshCwIcon,
  Trash2Icon,
  WebhookIcon,
} from "lucide-react";
import { createFileRoute } from "@tanstack/react-router";
import {
  useWebhooks,
  useCreateWebhook,
  useUpdateWebhook,
  useDeleteWebhook,
} from "~/lib/api/hooks/webhooks";
import type { Webhook } from "~/lib/api/types";
import { isApiError } from "~/lib/api/envelope";
import { Button } from "~/components/ui/button";
import { Badge } from "~/components/ui/badge";
import { Skeleton } from "~/components/ui/skeleton";
import { Card, CardContent } from "~/components/ui/card";
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
  DialogTrigger,
} from "~/components/ui/dialog";
import { toast } from "sonner";
import {
  WebhookForm,
  emptyForm,
  formFromWebhook,
  toRequest,
  validateWebhookForm,
  type FormState,
} from "./-webhook-editor";

export const Route = createFileRoute("/_app/user/webhooks")({
  component: Webhooks,
});

function Webhooks() {
  const webhooks = useWebhooks();
  const del = useDeleteWebhook();

  const [editing, setEditing] = useState<Webhook | null>(null);

  const doDelete = (id: string): void => {
    if (
      !window.confirm(
        "Delete this webhook? Events will stop being delivered to its URL immediately.",
      )
    )
      return;
    del.mutate(
      { id },
      {
        onError: (err) =>
          toast.error(isApiError(err) ? err.message : "Delete failed"),
        onSuccess: () => toast.success("Webhook deleted"),
      },
    );
  };

  const rows = webhooks.data?.pages.flatMap((p) => p.data) ?? [];

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between gap-2">
        <h1 className="text-xl font-semibold">Webhooks</h1>
        <CreateWebhookDialog />
      </div>

      <Card>
        <CardContent className="p-0">
          {webhooks.isLoading ? (
            <div className="space-y-2 p-4">
              <Skeleton className="h-10 w-full" />
              <Skeleton className="h-10 w-full" />
            </div>
          ) : webhooks.isError ? (
            <div className="flex flex-col items-center gap-3 py-10 text-center">
              <p className="text-sm text-destructive">
                {isApiError(webhooks.error)
                  ? webhooks.error.message
                  : "Failed to load webhooks"}
              </p>
              <Button
                variant="outline"
                size="sm"
                className="gap-1.5"
                onClick={() => void webhooks.refetch()}
              >
                <RefreshCwIcon className="size-4" aria-hidden />
                Retry
              </Button>
            </div>
          ) : (
            <WebhookTable
              rows={rows}
              deleting={del.isPending}
              onEdit={setEditing}
              onDelete={doDelete}
            />
          )}
        </CardContent>
      </Card>

      {webhooks.hasNextPage && (
        <div className="flex justify-center">
          <Button
            variant="outline"
            disabled={webhooks.isFetchingNextPage}
            onClick={() => void webhooks.fetchNextPage()}
          >
            {webhooks.isFetchingNextPage ? "Loading…" : "Load more"}
          </Button>
        </div>
      )}

      <EditWebhookDialog
        key={editing?.id ?? "none"}
        webhook={editing}
        onClose={() => setEditing(null)}
      />
    </div>
  );
}

function WebhookTable({
  rows,
  deleting,
  onEdit,
  onDelete,
}: {
  rows: Webhook[];
  deleting: boolean;
  onEdit: (w: Webhook) => void;
  onDelete: (id: string) => void;
}) {
  if (rows.length === 0) {
    return (
      <p className="py-10 text-center text-sm text-muted-foreground">
        No webhooks yet. Create one to receive events at your own URL.
      </p>
    );
  }

  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>URL</TableHead>
          <TableHead>Scope</TableHead>
          <TableHead>Events</TableHead>
          <TableHead>Status</TableHead>
          <TableHead className="text-right">Actions</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {rows.map((w) => {
          const events = w.events ?? [];
          const allEvents = events.length === 0 || events.includes("*");
          return (
            <TableRow key={w.id}>
              <TableCell className="max-w-[18rem] truncate font-mono text-xs">
                {w.url ?? "—"}
              </TableCell>
              <TableCell className="text-sm text-muted-foreground">
                {w.sessionId ? (
                  <span className="font-mono text-xs">{w.sessionId}</span>
                ) : (
                  <Badge variant="outline">all sessions</Badge>
                )}
              </TableCell>
              <TableCell>
                {allEvents ? (
                  <Badge variant="secondary">all events</Badge>
                ) : (
                  <div className="flex flex-wrap gap-1">
                    {events.slice(0, 3).map((e) => (
                      <Badge key={e} variant="secondary">
                        {e}
                      </Badge>
                    ))}
                    {events.length > 3 && (
                      <Badge variant="outline">+{events.length - 3}</Badge>
                    )}
                  </div>
                )}
              </TableCell>
              <TableCell>
                <Badge variant={w.active === false ? "outline" : "default"}>
                  {w.active === false ? "disabled" : "enabled"}
                </Badge>
              </TableCell>
              <TableCell className="text-right">
                <div className="flex justify-end gap-2">
                  <Button size="sm" variant="outline" onClick={() => onEdit(w)}>
                    Edit
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    className="text-destructive hover:text-destructive"
                    disabled={deleting}
                    onClick={() => onDelete(w.id ?? "")}
                  >
                    <Trash2Icon className="size-4" aria-hidden />
                  </Button>
                </div>
              </TableCell>
            </TableRow>
          );
        })}
      </TableBody>
    </Table>
  );
}
function CreateWebhookDialog() {
  const [open, setOpen] = useState(false);
  const [state, setState] = useState<FormState>(emptyForm);
  const create = useCreateWebhook();

  const submit = (e: React.FormEvent): void => {
    e.preventDefault();
    const error = validateWebhookForm(state);
    if (error) {
      toast.error(error);
      return;
    }
    create.mutate(toRequest(state), {
      onError: (err) =>
        toast.error(isApiError(err) ? err.message : "Failed to create webhook"),
      onSuccess: () => {
        toast.success("Webhook created");
        setState(emptyForm());
        setOpen(false);
      },
    });
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) setState(emptyForm());
      }}
    >
      <DialogTrigger asChild>
        <Button size="sm" className="gap-1.5">
          <PlusIcon className="size-4" aria-hidden />
          New webhook
        </Button>
      </DialogTrigger>
      <DialogContent className="max-h-[90vh] overflow-y-auto">
        <form onSubmit={submit}>
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <WebhookIcon className="size-5" aria-hidden />
              Create webhook
            </DialogTitle>
            <DialogDescription>
              Deliver WhatsApp events to your URL. Each delivery can be signed
              with an HMAC secret.
            </DialogDescription>
          </DialogHeader>

          <WebhookForm state={state} setState={setState} idPrefix="create" />

          <DialogFooter>
            <DialogClose asChild>
              <Button type="button" variant="outline">
                Cancel
              </Button>
            </DialogClose>
            <Button type="submit" disabled={create.isPending}>
              {create.isPending ? "Creating…" : "Create webhook"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function EditWebhookDialog({
  webhook,
  onClose,
}: {
  webhook: Webhook | null;
  onClose: () => void;
}) {
  // Keyed remount (parent passes key={editing.id}) gives each opened webhook a
  // fresh initial form.
  const [state, setState] = useState<FormState>(() =>
    webhook ? formFromWebhook(webhook) : emptyForm(),
  );
  const update = useUpdateWebhook();

  const submit = (e: React.FormEvent): void => {
    e.preventDefault();
    if (!webhook?.id) return;
    const error = validateWebhookForm(state);
    if (error) {
      toast.error(error);
      return;
    }
    update.mutate(
      { id: webhook.id, patch: toRequest(state) },
      {
        onError: (err) =>
          toast.error(
            isApiError(err) ? err.message : "Failed to update webhook",
          ),
        onSuccess: () => {
          toast.success("Webhook updated");
          onClose();
        },
      },
    );
  };

  return (
    <Dialog open={Boolean(webhook)} onOpenChange={(next) => !next && onClose()}>
      <DialogContent className="max-h-[90vh] overflow-y-auto">
        <form onSubmit={submit}>
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <WebhookIcon className="size-5" aria-hidden />
              Edit webhook
            </DialogTitle>
            <DialogDescription>
              Leave the HMAC secret blank to keep the existing one.
            </DialogDescription>
          </DialogHeader>

          <WebhookForm state={state} setState={setState} idPrefix="edit" />

          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={update.isPending}>
              {update.isPending ? "Saving…" : "Save changes"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
