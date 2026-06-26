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
import { KeyRoundIcon, PlusIcon, RefreshCwIcon } from "lucide-react";
import {
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { authClient } from "~/lib/auth/client";
import { useAppSession } from "~/lib/auth/context";
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
import { CopyButton, formatTimestamp } from "./-components/user-ui";

export const Route = createFileRoute("/_app/user/keys")({
  component: Keys,
});

// The gateway's capability model (§4.2). Stored under a single "wa" resource:
//   permissions: { wa: ["read","send",...] }
const GATEWAY_RESOURCE = "wa";
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

function toMs(d: string | Date | null | undefined): number | undefined {
  if (!d) return undefined;
  const t = typeof d === "string" ? Date.parse(d) : d.getTime();
  return Number.isNaN(t) ? undefined : t;
}

function Keys() {
  const session = useAppSession();
  const orgId = session.activeOrg?.id;
  const qc = useQueryClient();

  const keys = useQuery({
    queryKey: keysQueryKey(orgId),
    enabled: Boolean(orgId),
    queryFn: async (): Promise<ApiKeyRow[]> => {
      const { data, error } = await authClient.apiKey.list(
        orgId ? { query: { organizationId: orgId } } : undefined,
      );
      if (error) throw new Error(error.message ?? "Failed to load keys");
      return (data ?? []) as unknown as ApiKeyRow[];
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

  const doDelete = (id: string): void => {
    if (
      !window.confirm("Revoke this key? Applications using it will stop working.")
    )
      return;
    del.mutate(id, { onSuccess: () => toast.success("Key revoked") });
  };

  if (!orgId) {
    return (
      <Card>
        <CardContent className="py-10 text-center text-sm text-muted-foreground">
          Select an organization to manage its API keys.
        </CardContent>
      </Card>
    );
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between gap-2">
        <h1 className="text-xl font-semibold">API Keys</h1>
        <CreateKeyDialog orgId={orgId} onCreated={setSecret} />
      </div>

      <Card>
        <CardContent className="p-0">
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
              onDelete={doDelete}
            />
          )}
        </CardContent>
      </Card>

      <SecretDialog secret={secret} onClose={() => setSecret(null)} />
    </div>
  );
}

function KeyTable({
  rows,
  deleting,
  onDelete,
}: {
  rows: ApiKeyRow[];
  deleting: boolean;
  onDelete: (id: string) => void;
}) {
  if (rows.length === 0) {
    return (
      <p className="py-10 text-center text-sm text-muted-foreground">
        No API keys yet. Create one to call the gateway API from your own apps.
      </p>
    );
  }

  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>Name</TableHead>
          <TableHead>Prefix</TableHead>
          <TableHead>Permissions</TableHead>
          <TableHead>Last used</TableHead>
          <TableHead className="text-right">Actions</TableHead>
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
                {formatTimestamp(toMs(k.lastRequest))}
              </TableCell>
              <TableCell className="text-right">
                <Button
                  size="sm"
                  variant="ghost"
                  className="text-destructive hover:text-destructive"
                  disabled={deleting}
                  onClick={() => onDelete(k.id)}
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
      const { data, error } = await authClient.apiKey.create({
        name: name.trim(),
        organizationId: orgId,
        permissions: { [GATEWAY_RESOURCE]: selected },
      });
      if (error || !data) throw new Error(error?.message ?? "Failed to create key");
      return data as unknown as CreatedKey;
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
        <Button size="sm" className="gap-1.5">
          <PlusIcon className="size-4" aria-hidden />
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
  const value = secret?.key ?? "";
  return (
    <Dialog open={Boolean(secret)} onOpenChange={(next) => !next && onClose()}>
      <DialogContent showCloseButton={false}>
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <KeyRoundIcon className="size-5" aria-hidden />
            Save your API key
          </DialogTitle>
          <DialogDescription>
            This is the only time the full secret is shown. Copy it now — you
            won't be able to see it again.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-3 py-2">
          <div className="rounded-md border bg-muted/40 p-3">
            <code className="block text-sm break-all">{value}</code>
          </div>
          <CopyButton value={value} label="Copy key" className="w-full" />
        </div>

        <DialogFooter>
          <Button onClick={onClose}>I've saved it</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
