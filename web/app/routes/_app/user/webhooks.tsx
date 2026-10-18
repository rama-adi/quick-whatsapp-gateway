// User: webhooks CRUD — list, create, edit (PATCH), delete. §12 user surface,
// scoped to the active org (the gateway filters by the JWT's activeOrganizationId).
//
// Data + actions use the gateway webhook hooks (browser -> gateway, Bearer
// JWT): useWebhooks / useCreateWebhook / useUpdateWebhook / useDeleteWebhook.
// Webhooks are the gateway's own config resource (§13), so they stay on the
// gateway API (unlike API keys, which live in better-auth).
//
// The HMAC secret is write-only and never returned. When the user sets one, we
// show it once more after saving (with a verification snippet), since this is
// the last moment the dashboard can display it.

import { useState } from "react";
import {
  BuildingIcon,
  EllipsisIcon,
  FlaskConicalIcon,
  PencilIcon,
  PlusIcon,
  RefreshCwIcon,
  ShieldCheckIcon,
  Trash2Icon,
  WebhookIcon,
} from "lucide-react";
import { createFileRoute } from "@tanstack/react-router";
import { toast } from "sonner";
import {
  useWebhooks,
  useCreateWebhook,
  useUpdateWebhook,
  useDeleteWebhook,
} from "~/lib/api/hooks/webhooks";
import { useSessions } from "~/lib/api/hooks/sessions";
import type { Webhook } from "~/lib/api/types";
import { isApiError } from "~/lib/api/envelope";
import { useAppSession } from "~/lib/auth/context";
import { Button } from "~/components/ui/button";
import { Badge } from "~/components/ui/badge";
import { Skeleton } from "~/components/ui/skeleton";
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
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "~/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "~/components/ui/dropdown-menu";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "~/components/ui/tooltip";
import { Page, PageTitle } from "~/components/shell/page-title";
import { useConfirm } from "~/components/confirm-dialog";
import { SecretRevealDialog } from "~/components/secret-reveal";
import { CodeSnippet } from "~/components/code-snippet";
import { CopyButton, CopyableId } from "~/components/copyable";
import { SessionStatusBadge } from "~/components/status-badge";
import { Time } from "~/components/time";
import { DOCS } from "~/lib/docs-links";
import { titleHead } from "~/lib/head";
import { cn } from "~/lib/utils";
import {
  WebhookForm,
  emptyForm,
  eventTitle,
  formFromWebhook,
  hasAdvancedError,
  toRequest,
  webhookFieldErrors,
  type FieldErrors,
  type FormState,
} from "./-webhook-editor";
import { sessionTitle } from "~/lib/format";

export const Route = createFileRoute("/_app/user/webhooks")({
  head: () => titleHead("Webhooks"),
  component: Webhooks,
});

type EditorTarget = { mode: "create" } | { mode: "edit"; webhook: Webhook };

function Webhooks() {
  const session = useAppSession();
  const orgId = session.activeOrg?.id;
  const webhooks = useWebhooks();
  const del = useDeleteWebhook();
  const confirm = useConfirm();

  const [editor, setEditor] = useState<EditorTarget | null>(null);
  const [savedSecret, setSavedSecret] = useState<string | null>(null);
  const [testing, setTesting] = useState<Webhook | null>(null);

  const doDelete = async (w: Webhook): Promise<void> => {
    const ok = await confirm({
      title: <>Delete the webhook for {hostOf(w.url)}?</>,
      description: (
        <>
          Events stop being delivered to{" "}
          <span className="font-mono break-all">{w.url}</span> right away. This
          can't be undone.
        </>
      ),
      confirmLabel: "Delete webhook",
      destructive: true,
    });
    if (!ok) return;
    del.mutate(
      { id: w.id },
      {
        onError: (err) =>
          toast.error(isApiError(err) ? err.message : "Delete failed"),
        onSuccess: () => toast.success("Webhook deleted"),
      },
    );
  };

  const title = "Webhooks";
  const description =
    "Get WhatsApp events (new messages, receipts, status changes) POSTed to your own server.";

  if (!orgId) {
    return (
      <Page>
        <PageTitle title={title} description={description} docsHref={DOCS.receiving} />
        <Empty>
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <BuildingIcon aria-hidden />
            </EmptyMedia>
            <EmptyTitle>No organization selected</EmptyTitle>
            <EmptyDescription>
              Pick an organization in the switcher at the top of the page to
              manage its webhooks.
            </EmptyDescription>
          </EmptyHeader>
        </Empty>
      </Page>
    );
  }

  const rows = webhooks.data?.pages.flatMap((p) => p.data) ?? [];
  const newButton = (
    <Button onClick={() => setEditor({ mode: "create" })}>
      <PlusIcon aria-hidden data-icon="inline-start" />
      New webhook
    </Button>
  );

  return (
    <Page>
      <PageTitle
        title={title}
        description={description}
        docsHref={DOCS.receiving}
        actions={rows.length > 0 ? newButton : undefined}
      />

      {webhooks.isLoading ? (
        <TableCard>
          <div className="space-y-2 p-4">
            <Skeleton className="h-10 w-full" />
            <Skeleton className="h-10 w-full" />
          </div>
        </TableCard>
      ) : webhooks.isError ? (
        <TableCard>
          <div className="flex flex-col items-center gap-3 py-10 text-center">
            <p className="text-sm text-destructive">
              {isApiError(webhooks.error)
                ? webhooks.error.message
                : "Failed to load webhooks"}
            </p>
            <Button variant="outline" size="sm" onClick={() => void webhooks.refetch()}>
              <RefreshCwIcon aria-hidden data-icon="inline-start" />
              Retry
            </Button>
          </div>
        </TableCard>
      ) : rows.length === 0 ? (
        <Empty>
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <WebhookIcon aria-hidden />
            </EmptyMedia>
            <EmptyTitle>No webhooks yet</EmptyTitle>
            <EmptyDescription>
              A webhook sends each WhatsApp event to a URL on your server as it
              happens, so your app can react to new messages without polling.
            </EmptyDescription>
          </EmptyHeader>
          <EmptyContent>
            {newButton}
            <a
              href={DOCS.receiving}
              target="_blank"
              rel="noreferrer"
              className="text-xs text-muted-foreground underline-offset-4 hover:text-foreground hover:underline"
            >
              Read the guide to receiving events
            </a>
          </EmptyContent>
        </Empty>
      ) : (
        <TableCard>
          <WebhookTable
            rows={rows}
            deleting={del.isPending}
            onEdit={(webhook) => setEditor({ mode: "edit", webhook })}
            onTest={setTesting}
            onDelete={(w) => void doDelete(w)}
          />
        </TableCard>
      )}

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

      <WebhookDialog
        key={editor ? (editor.mode === "edit" ? editor.webhook.id : "create") : "closed"}
        target={editor}
        onClose={() => setEditor(null)}
        onSecretSaved={setSavedSecret}
      />

      <SecretRevealDialog
        open={Boolean(savedSecret)}
        secret={savedSecret ?? ""}
        title="Save your signing secret"
        icon={<ShieldCheckIcon className="size-5 text-muted-foreground" aria-hidden />}
        description="Add it to your server's environment (for example WEBHOOK_SECRET) and use it to verify the X-Webhook-Hmac header on every delivery."
        secretLabel="Signing secret"
        copyLabel="Copy secret"
        className="sm:max-w-2xl"
        onClose={() => setSavedSecret(null)}
      >
        <div className="grid gap-1.5">
          <span className="text-sm font-medium">Verify a delivery</span>
          <CodeSnippet tabs={verifySnippets()} />
        </div>
      </SecretRevealDialog>

      <TestWebhookDialog webhook={testing} onClose={() => setTesting(null)} />
    </Page>
  );
}

function hostOf(url: string | undefined): string {
  if (!url) return "this URL";
  try {
    return new URL(url).host;
  } catch {
    return url;
  }
}

function WebhookTable({
  rows,
  deleting,
  onEdit,
  onTest,
  onDelete,
}: {
  rows: Webhook[];
  deleting: boolean;
  onEdit: (w: Webhook) => void;
  onTest: (w: Webhook) => void;
  onDelete: (w: Webhook) => void;
}) {
  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>Endpoint</TableHead>
          <TableHead>Sessions</TableHead>
          <TableHead>Events</TableHead>
          <TableHead>Status</TableHead>
          <TableHead>Created</TableHead>
          <TableHead className="w-0 text-right">
            <span className="sr-only">Actions</span>
          </TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {rows.map((w) => (
          <TableRow key={w.id}>
            <TableCell className="max-w-[22rem] min-w-56">
              <div className="grid min-w-0 gap-0.5">
                <div className="group/url flex min-w-0 items-center gap-0.5">
                  <span title={w.url} className="truncate font-mono text-xs">
                    {w.url}
                  </span>
                  <span className="opacity-0 transition-opacity duration-150 ease-soft group-hover/url:opacity-100 focus-within:opacity-100">
                    <CopyButton value={w.url} label="Copy URL" iconOnly />
                  </span>
                </div>
                <CopyableId
                  value={w.id}
                  truncate
                  label="Copy webhook ID"
                  className="text-muted-foreground"
                />
              </div>
            </TableCell>
            <TableCell>
              {w.sessionId ? (
                <ScopedSession sessionId={w.sessionId} />
              ) : (
                <span className="text-sm text-muted-foreground">All sessions</span>
              )}
            </TableCell>
            <TableCell>
              <EventsSummary events={w.events ?? []} />
            </TableCell>
            <TableCell>
              <ActiveBadge active={w.active !== false} />
            </TableCell>
            <TableCell className="text-sm text-muted-foreground">
              <Time value={w.createdAt} />
            </TableCell>
            <TableCell className="text-right">
              <div className="flex justify-end gap-1">
                <Button size="sm" variant="outline" onClick={() => onEdit(w)}>
                  Edit
                </Button>
                <DropdownMenu>
                  <DropdownMenuTrigger asChild>
                    <Button size="icon-sm" variant="ghost" aria-label="More actions">
                      <EllipsisIcon aria-hidden />
                    </Button>
                  </DropdownMenuTrigger>
                  <DropdownMenuContent align="end" className="min-w-44">
                    <DropdownMenuItem onSelect={() => onEdit(w)}>
                      <PencilIcon aria-hidden />
                      Edit
                    </DropdownMenuItem>
                    <DropdownMenuItem onSelect={() => onTest(w)}>
                      <FlaskConicalIcon aria-hidden />
                      Send a test event
                    </DropdownMenuItem>
                    <DropdownMenuSeparator />
                    <DropdownMenuItem
                      variant="destructive"
                      disabled={deleting}
                      onSelect={() => onDelete(w)}
                    >
                      <Trash2Icon aria-hidden />
                      Delete webhook
                    </DropdownMenuItem>
                  </DropdownMenuContent>
                </DropdownMenu>
              </div>
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}

function ActiveBadge({ active }: { active: boolean }) {
  return (
    <Badge
      className={cn(
        "gap-1.5",
        active
          ? "bg-emerald-500/12 text-emerald-700 dark:text-emerald-400"
          : "bg-muted text-muted-foreground",
      )}
    >
      <span
        aria-hidden
        className={cn(
          "size-1.5 rounded-full",
          active ? "bg-emerald-500" : "bg-muted-foreground/60",
        )}
      />
      {active ? "Active" : "Paused"}
    </Badge>
  );
}

function EventsSummary({ events }: { events: string[] }) {
  if (events.includes("*")) {
    return <span className="text-sm">All events</span>;
  }
  if (events.length === 0) {
    return <span className="text-sm text-muted-foreground">None</span>;
  }
  const shown = events.slice(0, 2);
  const rest = events.length - shown.length;
  return (
    <TooltipProvider delayDuration={300}>
      <Tooltip>
        <TooltipTrigger asChild>
          <span className="flex max-w-64 min-w-40 flex-wrap items-center gap-1">
            {shown.map((e) => (
              <Badge key={e} variant="secondary">
                {eventTitle(e)}
              </Badge>
            ))}
            {rest > 0 && <Badge variant="outline">+{rest}</Badge>}
          </span>
        </TooltipTrigger>
        <TooltipContent className="max-w-72">
          <ul className="grid gap-0.5">
            {events.map((e) => (
              <li key={e}>
                {eventTitle(e)} <span className="font-mono opacity-70">{e}</span>
              </li>
            ))}
          </ul>
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
}

/** Resolve a scoped session id to its label + number from the sessions list. */
function ScopedSession({ sessionId }: { sessionId: string }) {
  const sessions = useSessions();
  const session = sessions.data?.pages
    .flatMap((p) => p.data)
    .find((s) => s.id === sessionId);
  if (!session) {
    return <CopyableId value={sessionId} truncate label="Copy session ID" />;
  }
  return (
    <div className="grid gap-0.5">
      <span className="flex items-center gap-2 text-sm">
        <span className="truncate">{sessionTitle(session)}</span>
        <SessionStatusBadge status={session.status} />
      </span>
      {session.phoneNumber ? (
        <span className="text-xs text-muted-foreground tabular-nums">
          +{session.phoneNumber}
        </span>
      ) : null}
    </div>
  );
}

function WebhookDialog({
  target,
  onClose,
  onSecretSaved,
}: {
  target: EditorTarget | null;
  onClose: () => void;
  onSecretSaved: (secret: string) => void;
}) {
  // Keyed remount (the parent changes `key` per target) gives each opening a
  // fresh form.
  const mode = target?.mode ?? "create";
  const webhook = target?.mode === "edit" ? target.webhook : null;
  const [state, setState] = useState<FormState>(() =>
    webhook ? formFromWebhook(webhook) : emptyForm(),
  );
  const [submitted, setSubmitted] = useState(false);
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const create = useCreateWebhook();
  const update = useUpdateWebhook();
  const pending = create.isPending || update.isPending;

  const errors: FieldErrors = submitted ? webhookFieldErrors(state) : {};

  const submit = (e: React.FormEvent): void => {
    e.preventDefault();
    setSubmitted(true);
    const found = webhookFieldErrors(state);
    if (Object.keys(found).length > 0) {
      if (hasAdvancedError(found)) setAdvancedOpen(true);
      return;
    }
    const body = toRequest(state);
    const done = (verb: string) => {
      toast.success(`Webhook ${verb}`);
      onClose();
      if (body.secret) onSecretSaved(body.secret);
    };
    const fail = (fallback: string) => (err: unknown) =>
      toast.error(isApiError(err) ? err.message : fallback);
    if (webhook) {
      update.mutate(
        { id: webhook.id, patch: body },
        { onError: fail("Failed to update webhook"), onSuccess: () => done("updated") },
      );
    } else {
      create.mutate(body, {
        onError: fail("Failed to create webhook"),
        onSuccess: () => done("created"),
      });
    }
  };

  return (
    <Dialog open={Boolean(target)} onOpenChange={(next) => !next && onClose()}>
      <DialogContent className="max-h-[calc(100svh-2rem)] overflow-y-auto sm:max-w-xl">
        <form onSubmit={submit} noValidate>
          <DialogHeader>
            <DialogTitle>{mode === "edit" ? "Edit webhook" : "New webhook"}</DialogTitle>
            <DialogDescription>
              {mode === "edit"
                ? "Changes apply to the next event."
                : "Choose where events go and which ones you want. You can change this later."}
            </DialogDescription>
          </DialogHeader>

          <WebhookForm
            state={state}
            setState={setState}
            mode={mode}
            errors={errors}
            advancedOpen={advancedOpen}
            onAdvancedOpenChange={setAdvancedOpen}
          />

          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {mode === "edit"
                ? pending
                  ? "Saving…"
                  : "Save changes"
                : pending
                  ? "Creating…"
                  : "Create webhook"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// --- snippets ---------------------------------------------------------------

function verifySnippets() {
  return [
    {
      id: "node",
      label: "Node.js",
      code: `import crypto from "node:crypto";

// rawBody: the exact request body bytes, before JSON parsing.
function isFromGateway(rawBody, signatureHeader) {
  const expected = crypto
    .createHmac("sha512", process.env.WEBHOOK_SECRET)
    .update(rawBody)
    .digest("hex");
  const got = String(signatureHeader ?? "");
  return (
    got.length === expected.length &&
    crypto.timingSafeEqual(Buffer.from(got), Buffer.from(expected))
  );
}

// isFromGateway(rawBody, req.headers["x-webhook-hmac"])`,
    },
    {
      id: "python",
      label: "Python",
      code: `import hashlib, hmac, os

# raw_body: the exact request body bytes, before JSON parsing.
def is_from_gateway(raw_body: bytes, signature_header: str) -> bool:
    expected = hmac.new(
        os.environ["WEBHOOK_SECRET"].encode(), raw_body, hashlib.sha512
    ).hexdigest()
    return hmac.compare_digest(expected, signature_header or "")

# is_from_gateway(request.body, request.headers.get("X-Webhook-Hmac"))`,
    },
  ];
}

const SAMPLE_EVENT = {
  schema: "v1",
  id: "evt_TEST0000000000000000000001",
  event: "message",
  session: "sess_TEST",
  organization: "org_TEST",
  timestamp: 1719400010000,
  payload: {
    waMessageId: "3EB0TEST",
    chatJid: "6281234567890@s.whatsapp.net",
    senderJid: "6281234567890@s.whatsapp.net",
    fromMe: false,
    type: "text",
    body: "Hello from the gateway test",
    hasMedia: false,
    media: null,
    timestamp: 1719400010000,
    pushName: "Test",
  },
};

const shellQuote = (s: string) => `'${s.replace(/'/g, `'\\''`)}'`;

function testSnippet(url: string): string {
  return `# Sends a sample "message" event to your endpoint, signed like a real delivery.
SECRET='your-signing-secret'   # the secret you saved for this webhook (any value if none)
BODY=${shellQuote(JSON.stringify(SAMPLE_EVENT))}
SIG=$(printf '%s' "$BODY" | openssl dgst -sha512 -hmac "$SECRET" | sed 's/^.*= //')

curl -X POST ${shellQuote(url)} \\
  -H 'Content-Type: application/json' \\
  -H 'X-Webhook-Request-Id: ${SAMPLE_EVENT.id}' \\
  -H "X-Webhook-Timestamp: $(date +%s)000" \\
  -H "X-Webhook-Hmac: $SIG" \\
  -H 'X-Webhook-Hmac-Algorithm: sha512' \\
  --data "$BODY"`;
}

function TestWebhookDialog({
  webhook,
  onClose,
}: {
  webhook: Webhook | null;
  onClose: () => void;
}) {
  return (
    <Dialog open={Boolean(webhook)} onOpenChange={(next) => !next && onClose()}>
      <DialogContent className="max-h-[calc(100svh-2rem)] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Send a test event</DialogTitle>
          <DialogDescription className="text-pretty">
            The dashboard can't trigger a delivery, but you can send your
            endpoint the same request the gateway would. Run this in a terminal;
            your server should answer with a 2xx status. To see real events,
            send a message to one of your numbers.
          </DialogDescription>
        </DialogHeader>
        {webhook ? (
          <CodeSnippet code={testSnippet(webhook.url)} title="Terminal" />
        ) : null}
        <p className="text-xs text-muted-foreground">
          Payload shapes for every event are in the{" "}
          <a
            href={DOCS.eventShape}
            target="_blank"
            rel="noreferrer"
            className="font-medium text-foreground/80 underline-offset-4 hover:text-foreground hover:underline"
          >
            event reference
          </a>
          .
        </p>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            Done
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
