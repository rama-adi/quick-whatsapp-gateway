// User: API keys — create (full secret shown ONCE), list, revoke. §12 user
// surface, scoped to the active organization.
//
// MAJOR RESHAPE from v1. v1 created/listed/revoked keys through the GATEWAY
// /keys endpoints (app/lib/api/hooks/keys.ts). In v2 the gateway no longer
// serves key management (§13 "Removed vs v1: /keys*"); API keys are owned by the
// FRONTEND via better-auth's org-scoped api-key plugin (@better-auth/api-key).
// So this route talks to the better-auth CLIENT (authClient.apiKey.*) at
// /api/auth/api-key/* — NOT the gateway. The gateway later *verifies* these keys
// against the shared `apikey` table (§4.2) and resolves the owning org from the
// key's `referenceId` (the api-key plugin's organization reference).
//
// Org-scoping: every call passes `organizationId` = the active org so keys are
// created/listed/revoked within the org the user is currently acting in (the
// gateway reads the org off the key row). Permissions map to the gateway's
// {read,send,manage,events} model, stored under a single "wa" resource so the
// gateway can read them back as a flat capability set.
//
// Secret hygiene: authClient.apiKey.create returns the full `key` ONCE; we hold
// it in component state just long enough to show the copy dialog, then drop it.
// list() never returns the secret (only `start`/`prefix`).

import { useState } from "react";
import { BuildingIcon, KeyRoundIcon, PlusIcon, RefreshCwIcon } from "lucide-react";
import {
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { createServerFn } from "@tanstack/react-start";
import { getRequest } from "@tanstack/react-start/server";
import { authClient } from "~/lib/auth/client";
import { useAppSession } from "~/lib/auth/context";
import { Button } from "~/components/ui/button";
import { Input } from "~/components/ui/input";
import { Label } from "~/components/ui/label";
import { Badge } from "~/components/ui/badge";
import { Skeleton } from "~/components/ui/skeleton";
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "~/components/ui/empty";
import { Page, PageTitle } from "~/components/shell/page-title";
import {
  Table,
  TableCard,
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
import { useConfirm } from "~/components/confirm-dialog";
import { SecretRevealDialog } from "~/components/secret-reveal";
import { CodeSnippet } from "~/components/code-snippet";
import { CopyableId } from "~/components/copyable";
import { Time } from "~/components/time";
import { usePublicApiUrl } from "~/lib/api/public-url";
import { apiRequestSnippets } from "~/lib/snippets";
import { DOCS } from "~/lib/docs-links";
import { titleHead } from "~/lib/head";

export const Route = createFileRoute("/_app/user/keys")({
  head: () => titleHead("API keys"),
  component: Keys,
});

// The gateway's capability model (§4.2). Stored under a single "gateway"
// resource: permissions: { gateway: ["read","send",...] }. This bucket name is
// load-bearing — the gateway parses scopes from the "gateway" key
// (internal/store/apikey.go parseAPIKeyPermissions), matching the api-key
// plugin's own defaultPermissions ({ gateway: ["read"] }) in app/lib/auth/server.ts.
const GATEWAY_RESOURCE = "gateway";
const PERMISSION_KEYS = ["read", "send", "manage", "events"] as const;
type PermissionKey = (typeof PERMISSION_KEYS)[number];

const PERMISSION_HINTS: Record<PermissionKey, string> = {
  read: "read chats, contacts, messages",
  send: "send messages",
  manage: "manage sessions, keys, webhooks",
  events: "subscribe to the event stream",
};

// Shapes returned by the api-key client (subset we render). `key` is present
// only on create; list rows expose `start`/`prefix` but never the secret.
type ApiKeyRow = {
  id: string;
  name: string | null;
  start: string | null;
  prefix: string | null;
  enabled: boolean;
  expiresAt: string | Date | null;
  createdAt: string | Date;
  lastRequest: string | Date | null;
  permissions?: Record<string, string[]> | null;
};
type CreatedKey = ApiKeyRow & { key: string };

const keysQueryKey = (orgId: string | undefined) =>
  ["auth", "api-keys", orgId] as const;

// CREATE runs SERVER-SIDE. The api-key plugin treats `permissions` (and the org
// reference) as SERVER_ONLY_PROPERTY: a create that carries `permissions` is
// rejected as privilege escalation whenever the call looks like a *client*
// request. Crucially, the plugin flags a call as a client request when it is
// given `headers` OR `request` (api-key plugin: `isClientRequest = ctx.request ||
// ctx.headers`). So we MUST NOT forward the incoming headers into createApiKey —
// doing so re-trips the guard. Instead we:
//   1. resolve the session ourselves from the request headers (to learn the
//      acting user + prove they're signed in), then
//   2. call createApiKey WITHOUT headers (a true server call) and pass `userId`
//      so the plugin can authorize the org membership and set referenceId = org.
// The auth server module is imported lazily inside the handler so its
// drizzle/mysql2/node deps never leak into the client bundle.
const createApiKeyFn = createServerFn({ method: "POST" })
  .validator(
    (input: { name: string; organizationId: string; permissions: string[] }) =>
      input,
  )
  .handler(async ({ data }): Promise<CreatedKey> => {
    const { auth } = await import("~/lib/auth/server");
    const request = getRequest();
    const session = await auth.api.getSession({ headers: request.headers });
    if (!session?.user) {
      throw new Error("Not authenticated");
    }
    const created = await auth.api.createApiKey({
      // No `headers`/`request` here -> server call -> server-only `permissions`
      // is allowed. `userId` lets the plugin authorize the org membership.
      body: {
        name: data.name,
        organizationId: data.organizationId,
        userId: session.user.id,
        permissions: { [GATEWAY_RESOURCE]: data.permissions },
      },
    });
    return created as unknown as CreatedKey;
  });

function Keys() {
  const session = useAppSession();
  const orgId = session.activeOrg?.id;
  const qc = useQueryClient();
  const confirm = useConfirm();

  const keys = useQuery({
    queryKey: keysQueryKey(orgId),
    enabled: Boolean(orgId),
    queryFn: async (): Promise<ApiKeyRow[]> => {
      const { data, error } = await authClient.apiKey.list(
        orgId ? { query: { organizationId: orgId } } : undefined,
      );
      if (error) throw new Error(error.message ?? "Failed to load keys");
      // The api-key plugin returns { apiKeys, total }, not a bare array.
      const rows = (data as { apiKeys?: ApiKeyRow[] } | null)?.apiKeys;
      return Array.isArray(rows) ? rows : [];
    },
  });

  const del = useMutation({
    mutationFn: async (id: string) => {
      const { error } = await authClient.apiKey.delete({ keyId: id });
      if (error) throw new Error(error.message ?? "Revoke failed");
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: keysQueryKey(orgId) });
    },
    onError: (err: Error) => toast.error(err.message),
  });

  // The one-time secret to surface in the reveal dialog.
  const [secret, setSecret] = useState<CreatedKey | null>(null);

  const doDelete = async (key: ApiKeyRow): Promise<void> => {
    const ok = await confirm({
      title: key.name ? <>Revoke “{key.name}”?</> : "Revoke this key?",
      description:
        "Apps using this key stop working immediately. This can't be undone.",
      confirmLabel: "Revoke key",
      destructive: true,
    });
    if (!ok) return;
    del.mutate(key.id, { onSuccess: () => toast.success("Key revoked") });
  };

  const title = "API keys";
  const description =
    "Let your servers call the gateway API on behalf of this organization.";

  if (!orgId) {
    return (
      <Page>
        <PageTitle title={title} description={description} docsHref={DOCS.apiKeys} />
        <Empty>
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <BuildingIcon aria-hidden />
            </EmptyMedia>
            <EmptyTitle>No organization selected</EmptyTitle>
            <EmptyDescription>
              Select an organization to manage its API keys.
            </EmptyDescription>
          </EmptyHeader>
        </Empty>
      </Page>
    );
  }

  return (
    <Page>
      <PageTitle
        title={title}
        description={description}
        docsHref={DOCS.apiKeys}
        actions={<CreateKeyDialog orgId={orgId} onCreated={setSecret} />}
      />

      <TableCard>
        {keys.isLoading ? (
          <div className="space-y-2 p-4">
            <Skeleton className="h-10 w-full" />
            <Skeleton className="h-10 w-full" />
          </div>
        ) : keys.isError ? (
          <div className="flex flex-col items-center gap-3 py-10 text-center">
            <p className="text-sm text-destructive">
              {keys.error instanceof Error
                ? keys.error.message
                : "Failed to load keys"}
            </p>
            <Button
              variant="outline"
              size="sm"
              className="gap-1.5"
              onClick={() => void keys.refetch()}
            >
              <RefreshCwIcon className="size-4" aria-hidden />
              Retry
            </Button>
          </div>
        ) : (
          <KeyTable
            rows={keys.data ?? []}
            deleting={del.isPending}
            onDelete={(key) => void doDelete(key)}
          />
        )}
      </TableCard>

      <SecretDialog secret={secret} onClose={() => setSecret(null)} />
    </Page>
  );
}

function KeyTable({
  rows,
  deleting,
  onDelete,
}: {
  rows: ApiKeyRow[];
  deleting: boolean;
  onDelete: (key: ApiKeyRow) => void;
}) {
  if (rows.length === 0) {
    return (
      <Empty className="border-0">
        <EmptyHeader>
          <EmptyMedia variant="icon">
            <KeyRoundIcon aria-hidden />
          </EmptyMedia>
          <EmptyTitle>No API keys yet</EmptyTitle>
          <EmptyDescription>
            Create one to call the gateway API from your own apps.
          </EmptyDescription>
        </EmptyHeader>
      </Empty>
    );
  }

  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>Name</TableHead>
          <TableHead>Prefix</TableHead>
          <TableHead>Permissions</TableHead>
          <TableHead>Created</TableHead>
          <TableHead>Last used</TableHead>
          <TableHead className="text-right">
            <span className="sr-only">Actions</span>
          </TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {rows.map((k) => {
          const granted = k.permissions?.[GATEWAY_RESOURCE] ?? [];
          const disabled = !k.enabled;
          return (
            <TableRow key={k.id}>
              <TableCell className="font-medium">
                {k.name || "—"}
                {disabled && (
                  <Badge variant="destructive" className="ml-2">
                    disabled
                  </Badge>
                )}
              </TableCell>
              <TableCell className="font-mono text-xs">
                {k.start ? `${k.start}…` : k.prefix ? `${k.prefix}…` : "—"}
              </TableCell>
              <TableCell>
                <div className="flex flex-wrap gap-1">
                  {PERMISSION_KEYS.filter((p) => granted.includes(p)).map((p) => (
                    <Badge key={p} variant="secondary">
                      {p}
                    </Badge>
                  ))}
                  {granted.length === 0 && (
                    <span className="text-xs text-muted-foreground">none</span>
                  )}
                </div>
              </TableCell>
              <TableCell className="text-sm text-muted-foreground">
                <Time value={k.createdAt} />
              </TableCell>
              <TableCell className="text-sm text-muted-foreground">
                <Time value={k.lastRequest} fallback="Never" />
              </TableCell>
              <TableCell className="text-right">
                <Button
                  size="sm"
                  variant="ghost"
                  className="text-destructive hover:bg-destructive/10 hover:text-destructive"
                  disabled={deleting}
                  onClick={() => onDelete(k)}
                >
                  Revoke
                </Button>
              </TableCell>
            </TableRow>
          );
        })}
      </TableBody>
    </Table>
  );
}

function CreateKeyDialog({
  orgId,
  onCreated,
}: {
  orgId: string;
  onCreated: (result: CreatedKey) => void;
}) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [perms, setPerms] = useState<Record<PermissionKey, boolean>>({
    read: true,
    send: false,
    manage: false,
    events: false,
  });

  const create = useMutation({
    mutationFn: async (): Promise<CreatedKey> => {
      const selected = PERMISSION_KEYS.filter((p) => perms[p]);
      // CREATE goes through the server fn: permissions are server-only on the
      // api-key plugin, so a client-side create is rejected (privilege guard).
      return createApiKeyFn({
        data: {
          name: name.trim(),
          organizationId: orgId,
          permissions: selected,
        },
      });
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: keysQueryKey(orgId) });
    },
  });

  const reset = (): void => {
    setName("");
    setPerms({ read: true, send: false, manage: false, events: false });
  };

  const submit = (e: React.FormEvent): void => {
    e.preventDefault();
    if (!name.trim()) {
      toast.error("Give the key a name.");
      return;
    }
    create.mutate(undefined, {
      onError: (err) =>
        toast.error(err instanceof Error ? err.message : "Failed to create key"),
      onSuccess: (result) => {
        onCreated(result);
        toast.success("Key created");
        reset();
        setOpen(false);
      },
    });
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
          New key
        </Button>
      </DialogTrigger>
      <DialogContent>
        <form onSubmit={submit}>
          <DialogHeader>
            <DialogTitle>Create API key</DialogTitle>
            <DialogDescription>
              The full secret is shown once after creation. Store it somewhere
              safe — it cannot be retrieved later. The key acts within your
              active organization.
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-4 py-4">
            <div className="space-y-2">
              <Label htmlFor="key-name">Name</Label>
              <Input
                id="key-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="e.g. Production server"
                autoFocus
              />
            </div>

            <fieldset className="space-y-2">
              <legend className="text-sm font-medium">Permissions</legend>
              {PERMISSION_KEYS.map((p) => (
                <label
                  key={p}
                  htmlFor={`perm-${p}`}
                  className="flex items-center gap-3 text-sm"
                >
                  <input
                    id={`perm-${p}`}
                    type="checkbox"
                    className="size-4 accent-primary"
                    checked={perms[p]}
                    onChange={(e) =>
                      setPerms((cur) => ({ ...cur, [p]: e.target.checked }))
                    }
                  />
                  <span className="capitalize">{p}</span>
                  <span className="text-xs text-muted-foreground">
                    {PERMISSION_HINTS[p]}
                  </span>
                </label>
              ))}
            </fieldset>
          </div>

          <DialogFooter>
            <DialogClose asChild>
              <Button type="button" variant="outline">
                Cancel
              </Button>
            </DialogClose>
            <Button type="submit" disabled={create.isPending}>
              {create.isPending ? "Creating…" : "Create key"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function SecretDialog({
  secret,
  onClose,
}: {
  secret: CreatedKey | null;
  onClose: () => void;
}) {
  const apiBase = usePublicApiUrl();
  const value = secret?.key ?? "";
  return (
    <SecretRevealDialog
      open={Boolean(secret)}
      secret={value}
      title="Save your API key"
      secretLabel={secret?.name ? `API key “${secret.name}”` : "API key"}
      copyLabel="Copy key"
      onClose={onClose}
    >
      <div className="grid gap-1.5">
        <div className="flex min-w-0 items-center justify-between gap-3 text-sm">
          <span className="font-medium">Try it</span>
          <span className="flex min-w-0 items-center gap-1.5 text-xs text-muted-foreground">
            API endpoint
            <CopyableId value={apiBase} label="Copy API endpoint" />
          </span>
        </div>
        <CodeSnippet
          tabs={apiRequestSnippets({ url: `${apiBase}/sessions`, token: value })}
        />
      </div>
    </SecretRevealDialog>
  );
}
