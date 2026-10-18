// User: attachment storage — the organization's S3-compatible bucket
// connections. A session picks one of these in its overview; the gateway then
// copies each new attachment into that bucket so it stays downloadable.
//
// Create and edit happen in one dialog. On edit, the destination (endpoint,
// region, bucket, addressing) is read-only because the API refuses to change
// it, and credentials are write-only: they're only sent when the user chooses
// "Replace credentials". Form logic lives in ./-storage-form (tested).

import { useState } from "react";
import { Link, createFileRoute } from "@tanstack/react-router";
import { useQueries } from "@tanstack/react-query";
import {
  BuildingIcon,
  EllipsisIcon,
  HardDriveIcon,
  PencilIcon,
  PlusIcon,
  RefreshCwIcon,
  Trash2Icon,
  TriangleAlertIcon,
} from "lucide-react";
import { toast } from "sonner";
import {
  useDeleteStorage,
  useSaveStorage,
  useStorageBuckets,
  type StorageBucket,
} from "~/lib/api/hooks/storage";
import { useSessions } from "~/lib/api/hooks/sessions";
import { apiUrl, fetchJSON } from "~/lib/api/client";
import type { WASession } from "~/lib/api/types";
import { useAppSession } from "~/lib/auth/context";
import { Button } from "~/components/ui/button";
import { Badge } from "~/components/ui/badge";
import { Input } from "~/components/ui/input";
import { Label } from "~/components/ui/label";
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
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "~/components/ui/select";
import { Page, PageTitle } from "~/components/shell/page-title";
import { useConfirm } from "~/components/confirm-dialog";
import { CopyableId } from "~/components/copyable";
import { DOCS } from "~/lib/docs-links";
import { titleHead } from "~/lib/head";
import { cn } from "~/lib/utils";
import {
  PROVIDERS,
  RETENTION_CHOICES,
  applyProvider,
  emptyStorageForm,
  fieldForServerError,
  guessProvider,
  providerById,
  retentionLabel,
  setRegion,
  storageFieldErrors,
  storageFormFromBucket,
  toStorageInput,
  type RetentionChoice,
  type StorageFieldErrors,
  type StorageFormState,
} from "./-storage-form";
import { sessionTitle } from "~/lib/format";

export const Route = createFileRoute("/_app/user/storage")({
  head: () => titleHead("Attachment storage"),
  component: Storage,
});

type EditorTarget = { mode: "create" } | { mode: "edit"; bucket: StorageBucket };

function Storage() {
  const session = useAppSession();
  const orgId = session.activeOrg?.id;
  const buckets = useStorageBuckets();
  const remove = useDeleteStorage();
  const confirm = useConfirm();
  const usage = useBucketUsage(Boolean(orgId));
  const [editor, setEditor] = useState<EditorTarget | null>(null);

  const title = "Attachment storage";
  const description =
    "Save photos, videos, voice notes and documents to your own S3-compatible bucket so they stay downloadable.";

  if (!orgId) {
    return (
      <Page>
        <PageTitle title={title} description={description} docsHref={DOCS.attachmentStorage} />
        <Empty>
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <BuildingIcon aria-hidden />
            </EmptyMedia>
            <EmptyTitle>No organization selected</EmptyTitle>
            <EmptyDescription>
              Pick an organization in the switcher at the top of the page to
              manage its storage.
            </EmptyDescription>
          </EmptyHeader>
        </Empty>
      </Page>
    );
  }

  const doRemove = async (bucket: StorageBucket): Promise<void> => {
    const ok = await confirm({
      title: <>Remove “{bucket.name}”?</>,
      description:
        "The gateway forgets this connection and its saved keys. Files already in the bucket are not deleted. You can only remove a connection that no session uses and whose stored files have finished expiring.",
      confirmLabel: "Remove connection",
      destructive: true,
    });
    if (!ok) return;
    remove.mutate(bucket.id, {
      onSuccess: () => toast.success("Storage connection removed"),
      onError: (e) =>
        toast.error(
          /unlink|cleanup/i.test(e.message)
            ? "This connection is still in use. Unlink it from every session, and wait for its stored files to expire, then try again."
            : e.message,
        ),
    });
  };

  const items = buckets.data?.items ?? [];
  const connectButton = (
    <Button onClick={() => setEditor({ mode: "create" })}>
      <PlusIcon aria-hidden data-icon="inline-start" />
      Connect a bucket
    </Button>
  );

  return (
    <Page>
      <PageTitle
        title={title}
        description={description}
        docsHref={DOCS.attachmentStorage}
        actions={items.length > 0 ? connectButton : undefined}
      />

      {buckets.isLoading ? (
        <div className="grid gap-4 lg:grid-cols-2">
          <Skeleton className="h-56 rounded-xl" />
          <Skeleton className="h-56 rounded-xl" />
        </div>
      ) : buckets.isError ? (
        <div
          role="alert"
          className="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-destructive/30 bg-destructive/5 px-4 py-3"
        >
          <p className="flex items-center gap-2 text-sm text-destructive">
            <TriangleAlertIcon className="size-4 shrink-0" aria-hidden />
            {buckets.error.message || "Couldn't load storage connections."}
          </p>
          <Button variant="outline" size="sm" onClick={() => void buckets.refetch()}>
            <RefreshCwIcon aria-hidden data-icon="inline-start" />
            Retry
          </Button>
        </div>
      ) : items.length === 0 ? (
        <Empty>
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <HardDriveIcon aria-hidden />
            </EmptyMedia>
            <EmptyTitle>No storage connected</EmptyTitle>
            <EmptyDescription>
              Without storage, media messages arrive with their details but no
              file you can download. Connect a bucket on AWS S3, Cloudflare R2,
              MinIO or any S3-compatible service, then pick it in each session's
              overview.
            </EmptyDescription>
          </EmptyHeader>
          <EmptyContent>
            {connectButton}
            <a
              href={DOCS.attachmentStorage}
              target="_blank"
              rel="noreferrer"
              className="text-xs text-muted-foreground underline-offset-4 hover:text-foreground hover:underline"
            >
              What permissions the bucket needs
            </a>
          </EmptyContent>
        </Empty>
      ) : (
        <>
          <div className="grid gap-4 lg:grid-cols-2">
            {items.map((bucket) => (
              <BucketCard
                key={bucket.id}
                bucket={bucket}
                usedBy={usage.byBucket.get(bucket.id) ?? []}
                usageKnown={usage.ready}
                removing={remove.isPending}
                onEdit={() => setEditor({ mode: "edit", bucket })}
                onRemove={() => void doRemove(bucket)}
              />
            ))}
          </div>
          <p className="text-sm text-muted-foreground">
            To start saving attachments, open a session and choose a connection
            under <span className="font-medium text-foreground">Attachment storage</span>{" "}
            in its overview.{" "}
            <Link
              to="/user/sessions"
              className="font-medium text-foreground/80 underline-offset-4 hover:text-foreground hover:underline"
            >
              Go to sessions
            </Link>
          </p>
        </>
      )}

      <BucketDialog
        key={editor ? (editor.mode === "edit" ? editor.bucket.id : "create") : "closed"}
        target={editor}
        onClose={() => setEditor(null)}
      />
    </Page>
  );
}

/** Which sessions use each bucket, from each session's storage binding.
 * Shares the query key with useSessionStorage, so the overview card's cache
 * is reused. */
function useBucketUsage(enabled: boolean) {
  const sessions = useSessions();
  const rows = enabled ? (sessions.data?.pages.flatMap((p) => p.data) ?? []) : [];
  const bindings = useQueries({
    queries: rows.map((s) => ({
      queryKey: ["sessions", s.id, "storage"],
      queryFn: () =>
        fetchJSON<{ bucketId: string | null }>(
          apiUrl(`/sessions/${encodeURIComponent(s.id)}/storage`),
        ),
    })),
  });
  const byBucket = new Map<string, WASession[]>();
  rows.forEach((s, i) => {
    const id = bindings[i]?.data?.bucketId;
    if (id) byBucket.set(id, [...(byBucket.get(id) ?? []), s]);
  });
  const ready =
    sessions.isSuccess && bindings.every((b) => b.isSuccess || b.isError);
  return { byBucket, ready };
}

function BucketCard({
  bucket,
  usedBy,
  usageKnown,
  removing,
  onEdit,
  onRemove,
}: {
  bucket: StorageBucket;
  usedBy: WASession[];
  usageKnown: boolean;
  removing: boolean;
  onEdit: () => void;
  onRemove: () => void;
}) {
  const provider = providerById(guessProvider(bucket.endpoint));
  return (
    <div className="flex min-w-0 flex-col rounded-xl border bg-card shadow-xs">
      <div className="flex items-start justify-between gap-3 px-4 pt-4 pb-3">
        <div className="flex min-w-0 items-center gap-3">
          <span className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-muted text-muted-foreground ring-1 ring-border">
            <HardDriveIcon className="size-4" aria-hidden />
          </span>
          <div className="grid min-w-0 gap-0.5">
            <span className="truncate font-medium">{bucket.name}</span>
            <span className="text-xs text-muted-foreground">
              {provider.id === "other" ? "S3-compatible" : provider.label}
            </span>
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-1">
          <Button size="sm" variant="outline" onClick={onEdit}>
            Edit
          </Button>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button size="icon-sm" variant="ghost" aria-label={`More actions for ${bucket.name}`}>
                <EllipsisIcon aria-hidden />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="min-w-44">
              <DropdownMenuItem onSelect={onEdit}>
                <PencilIcon aria-hidden />
                Edit
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem
                variant="destructive"
                disabled={removing || usedBy.length > 0}
                onSelect={onRemove}
              >
                <Trash2Icon aria-hidden />
                {usedBy.length > 0 ? "Remove (unlink sessions first)" : "Remove…"}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </div>

      <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-6 gap-y-2 border-t px-4 py-3 text-sm">
        <dt className="text-muted-foreground">Bucket</dt>
        <dd className="min-w-0 truncate font-mono text-xs leading-5">{bucket.bucket}</dd>
        <dt className="text-muted-foreground">Endpoint</dt>
        <dd className="min-w-0">
          <CopyableId value={bucket.endpoint} label="Copy endpoint" className="-my-0.5" />
        </dd>
        <dt className="text-muted-foreground">Region</dt>
        <dd className="min-w-0 truncate font-mono text-xs leading-5">
          {bucket.region}
          {bucket.pathStyle ? (
            <span className="ml-2 font-sans text-muted-foreground">· path-style</span>
          ) : null}
        </dd>
        <dt className="text-muted-foreground">Retention</dt>
        <dd>{retentionLabel(bucket.retentionDays)}</dd>
        <dt className="text-muted-foreground">Used by</dt>
        <dd className="min-w-0">
          {!usageKnown ? (
            <span className="text-muted-foreground">…</span>
          ) : usedBy.length === 0 ? (
            <span className="text-muted-foreground">No sessions yet</span>
          ) : (
            <span className="flex flex-wrap gap-1">
              {usedBy.map((s) => (
                <Badge key={s.id} variant="secondary" asChild>
                  <Link to="/user/sessions/$sessionId" params={{ sessionId: s.id }}>
                    {sessionTitle(s)}
                  </Link>
                </Badge>
              ))}
            </span>
          )}
        </dd>
      </dl>

      <div className="mt-auto flex items-center justify-between gap-2 rounded-b-xl border-t bg-muted/30 px-4 py-2 text-xs text-muted-foreground">
        <span>Connection ID</span>
        <CopyableId value={bucket.id} truncate label="Copy connection ID" />
      </div>
    </div>
  );
}

function BucketDialog({
  target,
  onClose,
}: {
  target: EditorTarget | null;
  onClose: () => void;
}) {
  const mode = target?.mode ?? "create";
  const original = target?.mode === "edit" ? target.bucket : undefined;
  const [state, setState] = useState<StorageFormState>(() =>
    original ? storageFormFromBucket(original) : emptyStorageForm(),
  );
  const [submitted, setSubmitted] = useState(false);
  const [serverErrors, setServerErrors] = useState<StorageFieldErrors>({});
  const save = useSaveStorage();

  const errors: StorageFieldErrors = {
    ...serverErrors,
    ...(submitted ? storageFieldErrors(state, mode) : {}),
  };
  const update = (patch: Partial<StorageFormState>) => {
    setState((c) => ({ ...c, ...patch }));
    // A server error is about the old value; clear it once the field changes.
    setServerErrors((cur) => {
      const next = { ...cur };
      for (const k of Object.keys(patch)) delete next[k as keyof StorageFieldErrors];
      return next;
    });
  };

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    setSubmitted(true);
    if (Object.keys(storageFieldErrors(state, mode)).length > 0) return;
    save.mutate(
      { id: original?.id, body: toStorageInput(state, original) },
      {
        onSuccess: () => {
          toast.success(mode === "edit" ? "Storage connection saved" : "Bucket connected");
          onClose();
        },
        onError: (err) => {
          const field = fieldForServerError(err.message);
          if (field) setServerErrors({ [field]: capitalize(err.message) });
          else toast.error(err.message);
        },
      },
    );
  };

  const provider = providerById(state.provider);

  return (
    <Dialog open={Boolean(target)} onOpenChange={(next) => !next && onClose()}>
      <DialogContent className="max-h-[calc(100svh-2rem)] overflow-y-auto sm:max-w-xl">
        <form onSubmit={submit} noValidate>
          <DialogHeader>
            <DialogTitle>{mode === "edit" ? `Edit “${original?.name}”` : "Connect a bucket"}</DialogTitle>
            <DialogDescription className="text-pretty">
              {mode === "edit"
                ? "Rename the connection, rotate its keys or change how long new attachments are kept."
                : "The gateway uploads attachments here. The bucket stays private; files are served through expiring links."}
            </DialogDescription>
          </DialogHeader>

          <div className="grid gap-5 py-4">
            {mode === "create" ? (
              <div className="grid gap-1.5">
                <Label id="provider-label">Provider</Label>
                <div
                  role="radiogroup"
                  aria-labelledby="provider-label"
                  className="grid grid-cols-2 gap-1 rounded-lg bg-muted p-[3px] sm:grid-cols-4"
                >
                  {PROVIDERS.map((p) => (
                    <button
                      key={p.id}
                      type="button"
                      role="radio"
                      aria-checked={state.provider === p.id}
                      onClick={() => setState((c) => applyProvider(c, p.id))}
                      className={cn(
                        "h-7 rounded-md px-2 text-sm font-medium text-muted-foreground transition-all duration-150 ease-soft hover:text-foreground",
                        state.provider === p.id && "bg-background text-foreground shadow-xs",
                      )}
                    >
                      {p.label}
                    </button>
                  ))}
                </div>
              </div>
            ) : null}

            <Field label="Connection name" htmlFor="st-name" error={errors.name}>
              <Input
                id="st-name"
                value={state.name}
                onChange={(e) => update({ name: e.target.value })}
                placeholder="e.g. Production media"
                autoFocus={mode === "create"}
                aria-invalid={Boolean(errors.name) || undefined}
              />
            </Field>

            {mode === "create" ? (
              <>
                <div className="grid gap-4 sm:grid-cols-2">
                  <Field label="Bucket name" htmlFor="st-bucket" error={errors.bucket}>
                    <Input
                      id="st-bucket"
                      value={state.bucket}
                      onChange={(e) => update({ bucket: e.target.value })}
                      placeholder="my-whatsapp-media"
                      spellCheck={false}
                      className="font-mono text-xs"
                      aria-invalid={Boolean(errors.bucket) || undefined}
                    />
                  </Field>
                  <Field label="Region" htmlFor="st-region" error={errors.region}>
                    <Input
                      id="st-region"
                      value={state.region}
                      onChange={(e) => {
                        setState((c) => setRegion(c, e.target.value));
                        setServerErrors({});
                      }}
                      placeholder={provider.regionPlaceholder}
                      spellCheck={false}
                      className="font-mono text-xs"
                      aria-invalid={Boolean(errors.region) || undefined}
                    />
                  </Field>
                </div>
                <Field
                  label="Endpoint"
                  htmlFor="st-endpoint"
                  error={errors.endpoint}
                  hint={provider.endpointHint}
                >
                  <Input
                    id="st-endpoint"
                    type="url"
                    inputMode="url"
                    value={state.endpoint}
                    onChange={(e) => update({ endpoint: e.target.value, endpointTouched: true })}
                    placeholder={provider.endpointPlaceholder}
                    spellCheck={false}
                    className="font-mono text-xs"
                    aria-invalid={Boolean(errors.endpoint) || undefined}
                  />
                </Field>
                <label htmlFor="st-path" className="flex cursor-pointer items-start gap-3">
                  <input
                    id="st-path"
                    type="checkbox"
                    className="mt-0.5 size-4 accent-primary"
                    checked={state.pathStyle}
                    onChange={(e) => update({ pathStyle: e.target.checked })}
                  />
                  <span className="grid gap-0.5">
                    <span className="text-sm font-medium">Use path-style requests</span>
                    <span className="text-xs text-muted-foreground">
                      Needed by MinIO and most self-hosted S3. Leave off for AWS
                      and Cloudflare R2.
                    </span>
                  </span>
                </label>
              </>
            ) : original ? (
              <div className="grid gap-1.5">
                <Label>Destination</Label>
                <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-6 gap-y-1.5 rounded-lg border bg-muted/30 px-3 py-2.5 text-sm">
                  <dt className="text-muted-foreground">Bucket</dt>
                  <dd className="truncate font-mono text-xs leading-5">{original.bucket}</dd>
                  <dt className="text-muted-foreground">Endpoint</dt>
                  <dd className="truncate font-mono text-xs leading-5" title={original.endpoint}>
                    {original.endpoint}
                  </dd>
                  <dt className="text-muted-foreground">Region</dt>
                  <dd className="truncate font-mono text-xs leading-5">
                    {original.region}
                    {original.pathStyle ? (
                      <span className="ml-2 font-sans text-muted-foreground">· path-style</span>
                    ) : null}
                  </dd>
                </dl>
                <p className="text-xs text-muted-foreground">
                  To store files somewhere else, connect a new bucket. Files
                  already saved stay where they are.
                </p>
              </div>
            ) : null}

            <CredentialsFields mode={mode} state={state} update={update} errors={errors} />

            <RetentionField
              mode={mode}
              state={state}
              update={update}
              error={errors.retentionDays}
            />
          </div>

          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={save.isPending}>
              {save.isPending
                ? "Saving…"
                : mode === "edit"
                  ? "Save changes"
                  : "Connect bucket"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function CredentialsFields({
  mode,
  state,
  update,
  errors,
}: {
  mode: "create" | "edit";
  state: StorageFormState;
  update: (patch: Partial<StorageFormState>) => void;
  errors: StorageFieldErrors;
}) {
  if (mode === "edit" && !state.replaceCredentials) {
    return (
      <div className="grid gap-1.5">
        <Label>Access keys</Label>
        <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border bg-muted/30 px-3 py-2">
          <span className="text-sm text-muted-foreground">
            Saved and encrypted <span className="tracking-widest">••••••</span>
          </span>
          <Button
            type="button"
            size="sm"
            variant="outline"
            onClick={() => update({ replaceCredentials: true })}
          >
            Replace keys
          </Button>
        </div>
      </div>
    );
  }
  return (
    <div className="grid gap-1.5">
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Access key ID" htmlFor="st-ak" error={errors.accessKey}>
          <Input
            id="st-ak"
            value={state.accessKey}
            onChange={(e) => update({ accessKey: e.target.value })}
            placeholder="AKIA…"
            autoComplete="off"
            spellCheck={false}
            className="font-mono text-xs"
            aria-invalid={Boolean(errors.accessKey) || undefined}
          />
        </Field>
        <Field label="Secret access key" htmlFor="st-sk" error={errors.secretKey}>
          <Input
            id="st-sk"
            type="password"
            value={state.secretKey}
            onChange={(e) => update({ secretKey: e.target.value })}
            autoComplete="new-password"
            className="font-mono text-xs"
            aria-invalid={Boolean(errors.secretKey) || undefined}
          />
        </Field>
      </div>
      <p className="text-xs text-pretty text-muted-foreground">
        {mode === "edit" ? (
          <>
            Enter both new keys. Keep the old keys valid until files stored with
            them have expired.{" "}
            <button
              type="button"
              className="font-medium text-foreground/80 underline-offset-4 hover:text-foreground hover:underline"
              onClick={() => update({ replaceCredentials: false, accessKey: "", secretKey: "" })}
            >
              Keep the saved keys
            </button>
          </>
        ) : (
          "The keys need read, write, delete and list access to the bucket. They're stored encrypted and never shown again."
        )}
      </p>
    </div>
  );
}

function RetentionField({
  mode,
  state,
  update,
  error,
}: {
  mode: "create" | "edit";
  state: StorageFormState;
  update: (patch: Partial<StorageFormState>) => void;
  error?: string;
}) {
  return (
    <div className="grid gap-1.5">
      <Label htmlFor="st-retention">Keep attachments for</Label>
      <div className="flex flex-wrap items-center gap-2">
        <Select
          value={state.retention}
          onValueChange={(v) => update({ retention: v as RetentionChoice })}
        >
          <SelectTrigger id="st-retention" className="w-40">
            <SelectValue />
          </SelectTrigger>
          <SelectContent position="popper">
            {RETENTION_CHOICES.map((c) => (
              <SelectItem key={c.value} value={c.value}>
                {c.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {state.retention === "custom" ? (
          <div className="flex items-center gap-2">
            <Input
              id="st-days"
              type="number"
              inputMode="numeric"
              min={1}
              step={1}
              value={state.retentionDays}
              onChange={(e) => update({ retentionDays: e.target.value })}
              placeholder="14"
              className="w-24"
              aria-label="Days to keep attachments"
              aria-invalid={Boolean(error) || undefined}
              autoFocus
            />
            <span className="text-sm text-muted-foreground">days</span>
          </div>
        ) : null}
      </div>
      {error ? (
        <p className="text-xs text-destructive">{error}</p>
      ) : (
        <p className="text-xs text-pretty text-muted-foreground">
          {state.retention === "forever"
            ? "Files stay in the bucket until you delete them yourself."
            : "Counted from when each attachment is saved; the file is then deleted and its link stops working."}
          {mode === "edit" ? " Changes apply to new attachments only." : null}
        </p>
      )}
    </div>
  );
}

function Field({
  label,
  htmlFor,
  hint,
  error,
  children,
}: {
  label: string;
  htmlFor: string;
  hint?: string;
  error?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="grid min-w-0 content-start gap-1.5">
      <Label htmlFor={htmlFor}>{label}</Label>
      {children}
      {error ? (
        <p className="text-xs text-destructive">{error}</p>
      ) : hint ? (
        <p className="text-xs text-pretty text-muted-foreground">{hint}</p>
      ) : null}
    </div>
  );
}

function capitalize(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1);
}
