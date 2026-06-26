// User: webhooks CRUD — list, create, edit (PATCH), delete.
// Surface agent: user.
//
// Webhooks deliver the §9 event catalog to an external URL. A webhook may scope
// to a single session or fire for all tenant sessions (sessionId omitted), and
// may subscribe to all events ("*") or a comma-list of event types. The HMAC
// secret and custom headers are write-only on the request and never returned in
// the list response, so the form leaves them blank when editing (submitting a
// blank secret/headers leaves the stored values untouched server-side).

import { useState } from "react";
import {
  PlusIcon,
  RefreshCwIcon,
  Trash2Icon,
  WebhookIcon,
  XIcon,
} from "lucide-react";
import { requireUserPanel } from "./_guard";
import {
  useWebhooks,
  useCreateWebhook,
  useUpdateWebhook,
  useDeleteWebhook,
} from "~/lib/api/hooks/webhooks";
import type { Webhook, WebhookRequest } from "~/lib/api/types";
import { isApiError } from "~/lib/api/envelope";
import { Button } from "~/components/ui/button";
import { Input } from "~/components/ui/input";
import { Label } from "~/components/ui/label";
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

export const clientLoader = requireUserPanel;

// §9 event catalog (v1). "*" subscribes to everything.
const EVENT_CATALOG = [
  "session.status",
  "auth.qr",
  "auth.code",
  "message",
  "message.from_me",
  "message.status",
  "message.reaction",
  "message.edited",
  "message.revoked",
  "poll.vote",
  "presence.update",
  "group.update",
  "group.participant",
  "chat.update",
  "contact.update",
  "call.incoming",
  "newsletter.update",
] as const;

export default function Webhooks() {
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
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => onEdit(w)}
                  >
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

// --- shared form state ----------------------------------------------------

type HeaderRow = { key: string; value: string };

type FormState = {
  url: string;
  sessionId: string;
  allEvents: boolean;
  events: Set<string>;
  secret: string;
  headers: HeaderRow[];
  active: boolean;
};

function emptyForm(): FormState {
  return {
    url: "",
    sessionId: "",
    allEvents: true,
    events: new Set(),
    secret: "",
    headers: [],
    active: true,
  };
}

function formFromWebhook(w: Webhook): FormState {
  const events = w.events ?? [];
  const allEvents = events.length === 0 || events.includes("*");
  return {
    url: w.url ?? "",
    sessionId: w.sessionId ?? "",
    allEvents,
    events: new Set(allEvents ? [] : events),
    // secret is write-only and never returned; leave blank on edit.
    secret: "",
    headers: Object.entries(w.customHeaders ?? {}).map(([key, value]) => ({
      key,
      value,
    })),
    active: w.active !== false,
  };
}

// Build the WebhookRequest body, omitting blank optional fields so a PATCH
// doesn't clobber server-held write-only values (secret) it can't echo back.
function toRequest(s: FormState): WebhookRequest {
  const body: WebhookRequest = {
    url: s.url.trim(),
    events: s.allEvents ? ["*"] : [...s.events],
    active: s.active,
  };
  const sessionId = s.sessionId.trim();
  if (sessionId) body.sessionId = sessionId;
  const secret = s.secret.trim();
  if (secret) body.secret = secret;
  const headers = s.headers.filter((h) => h.key.trim());
  if (headers.length > 0) {
    body.customHeaders = Object.fromEntries(
      headers.map((h) => [h.key.trim(), h.value]),
    );
  }
  return body;
}

function validate(s: FormState): string | null {
  const url = s.url.trim();
  if (!url) return "Enter a delivery URL.";
  try {
    const parsed = new URL(url);
    if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
      return "URL must use http or https.";
    }
  } catch {
    return "Enter a valid URL.";
  }
  if (!s.allEvents && s.events.size === 0) {
    return "Select at least one event, or choose all events.";
  }
  if (s.headers.some((h) => !h.key.trim() && h.value.trim())) {
    return "Custom header rows need a name.";
  }
  return null;
}

function WebhookForm({
  state,
  setState,
  idPrefix,
}: {
  state: FormState;
  setState: React.Dispatch<React.SetStateAction<FormState>>;
  idPrefix: string;
}) {
  const toggleEvent = (e: string, checked: boolean): void =>
    setState((cur) => {
      const events = new Set(cur.events);
      if (checked) events.add(e);
      else events.delete(e);
      return { ...cur, events };
    });

  return (
    <div className="space-y-4 py-4">
      <div className="space-y-2">
        <Label htmlFor={`${idPrefix}-url`}>Delivery URL</Label>
        <Input
          id={`${idPrefix}-url`}
          type="url"
          value={state.url}
          onChange={(e) => setState((c) => ({ ...c, url: e.target.value }))}
          placeholder="https://example.com/webhooks/wa"
          autoFocus
        />
      </div>

      <div className="space-y-2">
        <Label htmlFor={`${idPrefix}-session`}>Session ID (optional)</Label>
        <Input
          id={`${idPrefix}-session`}
          value={state.sessionId}
          onChange={(e) =>
            setState((c) => ({ ...c, sessionId: e.target.value }))
          }
          placeholder="Leave blank for all your sessions"
          className="font-mono text-xs"
        />
      </div>

      <fieldset className="space-y-2">
        <legend className="text-sm font-medium">Events</legend>
        <label className="flex items-center gap-3 text-sm">
          <input
            type="checkbox"
            className="size-4 accent-primary"
            checked={state.allEvents}
            onChange={(e) =>
              setState((c) => ({ ...c, allEvents: e.target.checked }))
            }
          />
          <span>All events</span>
        </label>
        {!state.allEvents && (
          <div className="grid max-h-48 grid-cols-1 gap-1.5 overflow-y-auto rounded-md border p-3 sm:grid-cols-2">
            {EVENT_CATALOG.map((e) => (
              <label
                key={e}
                htmlFor={`${idPrefix}-evt-${e}`}
                className="flex items-center gap-2 text-sm"
              >
                <input
                  id={`${idPrefix}-evt-${e}`}
                  type="checkbox"
                  className="size-4 accent-primary"
                  checked={state.events.has(e)}
                  onChange={(ev) => toggleEvent(e, ev.target.checked)}
                />
                <span className="font-mono text-xs">{e}</span>
              </label>
            ))}
          </div>
        )}
      </fieldset>

      <div className="space-y-2">
        <Label htmlFor={`${idPrefix}-secret`}>HMAC secret (optional)</Label>
        <Input
          id={`${idPrefix}-secret`}
          type="password"
          value={state.secret}
          onChange={(e) => setState((c) => ({ ...c, secret: e.target.value }))}
          placeholder="Used to sign delivery payloads"
          autoComplete="off"
        />
      </div>

      <fieldset className="space-y-2">
        <legend className="text-sm font-medium">
          Custom headers (optional)
        </legend>
        <div className="space-y-2">
          {state.headers.map((h, i) => (
            <div key={i} className="flex items-center gap-2">
              <Input
                value={h.key}
                onChange={(e) =>
                  setState((c) => ({
                    ...c,
                    headers: c.headers.map((row, j) =>
                      j === i ? { ...row, key: e.target.value } : row,
                    ),
                  }))
                }
                placeholder="Header name"
                aria-label="Header name"
              />
              <Input
                value={h.value}
                onChange={(e) =>
                  setState((c) => ({
                    ...c,
                    headers: c.headers.map((row, j) =>
                      j === i ? { ...row, value: e.target.value } : row,
                    ),
                  }))
                }
                placeholder="Value"
                aria-label="Header value"
              />
              <Button
                type="button"
                size="icon"
                variant="ghost"
                aria-label="Remove header"
                onClick={() =>
                  setState((c) => ({
                    ...c,
                    headers: c.headers.filter((_, j) => j !== i),
                  }))
                }
              >
                <XIcon className="size-4" aria-hidden />
              </Button>
            </div>
          ))}
          <Button
            type="button"
            size="sm"
            variant="outline"
            className="gap-1.5"
            onClick={() =>
              setState((c) => ({
                ...c,
                headers: [...c.headers, { key: "", value: "" }],
              }))
            }
          >
            <PlusIcon className="size-4" aria-hidden />
            Add header
          </Button>
        </div>
      </fieldset>

      <label className="flex items-center gap-3 text-sm">
        <input
          type="checkbox"
          className="size-4 accent-primary"
          checked={state.active}
          onChange={(e) =>
            setState((c) => ({ ...c, active: e.target.checked }))
          }
        />
        <span>Enabled</span>
      </label>
    </div>
  );
}

function CreateWebhookDialog() {
  const [open, setOpen] = useState(false);
  const [state, setState] = useState<FormState>(emptyForm);
  const create = useCreateWebhook();

  const submit = (e: React.FormEvent): void => {
    e.preventDefault();
    const error = validate(state);
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
  // Keyed remount (below) gives each opened webhook a fresh initial form.
  const [state, setState] = useState<FormState>(() =>
    webhook ? formFromWebhook(webhook) : emptyForm(),
  );
  const update = useUpdateWebhook();

  const submit = (e: React.FormEvent): void => {
    e.preventDefault();
    if (!webhook?.id) return;
    const error = validate(state);
    if (error) {
      toast.error(error);
      return;
    }
    update.mutate(
      { id: webhook.id, patch: toRequest(state) },
      {
        onError: (err) =>
          toast.error(isApiError(err) ? err.message : "Failed to update webhook"),
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
