// User: API keys — create (full secret shown ONCE), list, rotate, delete.
// Surface agent: user.
//
// The full secret is returned only by POST /keys and POST /keys/{id}:rotate and
// is NEVER cached — we hold it in component state just long enough to show the
// copy dialog, then drop it. The list only ever shows the key prefix.

import { useState } from "react";
import { KeyRoundIcon, PlusIcon, RefreshCwIcon } from "lucide-react";
import { requireUserPanel } from "./_guard";
import {
  useKeys,
  useCreateKey,
  useRotateKey,
  useDeleteKey,
} from "~/lib/api/hooks/keys";
import type {
  ApiKey,
  CreateKeyRequest,
  CreateKeyResult,
  Permissions,
} from "~/lib/api/types";
import { isApiError } from "~/lib/api/envelope";
import { Button } from "~/components/ui/button";
import { Input } from "~/components/ui/input";
import { Label } from "~/components/ui/label";
import { Badge } from "~/components/ui/badge";
import { Skeleton } from "~/components/ui/skeleton";
import {
  Card,
  CardContent,
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
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "~/components/ui/select";
import { toast } from "sonner";
import { CopyButton, formatTimestamp } from "./_ui";

export const clientLoader = requireUserPanel;

const PERMISSION_KEYS: (keyof Permissions)[] = ["read", "send", "manage", "events"];

export default function Keys() {
  const keys = useKeys();
  const rotate = useRotateKey();
  const del = useDeleteKey();

  // The one-time secret to surface in the reveal dialog (create OR rotate).
  const [secret, setSecret] = useState<CreateKeyResult | null>(null);

  const doRotate = (id: string): void => {
    if (
      !window.confirm(
        "Rotate this key? The old secret stops working immediately and a new one is shown once.",
      )
    )
      return;
    rotate.mutate(
      { id },
      {
        onError: (err) =>
          toast.error(isApiError(err) ? err.message : "Rotate failed"),
        onSuccess: (result) => {
          setSecret(result);
          toast.success("Key rotated");
        },
      },
    );
  };

  const doDelete = (id: string): void => {
    if (!window.confirm("Revoke this key? Applications using it will stop working."))
      return;
    del.mutate(
      { id },
      {
        onError: (err) =>
          toast.error(isApiError(err) ? err.message : "Revoke failed"),
        onSuccess: () => toast.success("Key revoked"),
      },
    );
  };

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between gap-2">
        <h1 className="text-xl font-semibold">API Keys</h1>
        <CreateKeyDialog onCreated={setSecret} />
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
                {isApiError(keys.error)
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
              rows={keys.data?.pages.flatMap((p) => p.data) ?? []}
              rotating={rotate.isPending}
              deleting={del.isPending}
              onRotate={doRotate}
              onDelete={doDelete}
            />
          )}
        </CardContent>
      </Card>

      {keys.hasNextPage && (
        <div className="flex justify-center">
          <Button
            variant="outline"
            disabled={keys.isFetchingNextPage}
            onClick={() => void keys.fetchNextPage()}
          >
            {keys.isFetchingNextPage ? "Loading…" : "Load more"}
          </Button>
        </div>
      )}

      <SecretDialog secret={secret} onClose={() => setSecret(null)} />
    </div>
  );
}

function KeyTable({
  rows,
  rotating,
  deleting,
  onRotate,
  onDelete,
}: {
  rows: ApiKey[];
  rotating: boolean;
  deleting: boolean;
  onRotate: (id: string) => void;
  onDelete: (id: string) => void;
}) {
  if (rows.length === 0) {
    return (
      <p className="py-10 text-center text-sm text-muted-foreground">
        No API keys yet. Create one to call the API from your own apps.
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
          const revoked = Boolean(k.revokedAt);
          return (
            <TableRow key={k.id}>
              <TableCell className="font-medium">
                {k.name}
                {revoked && (
                  <Badge variant="destructive" className="ml-2">
                    revoked
                  </Badge>
                )}
              </TableCell>
              <TableCell className="font-mono text-xs">
                {k.keyPrefix ? `${k.keyPrefix}…` : "—"}
              </TableCell>
              <TableCell>
                <div className="flex flex-wrap gap-1">
                  {PERMISSION_KEYS.filter((p) => k.permissions?.[p]).map((p) => (
                    <Badge key={p} variant="secondary">
                      {p}
                    </Badge>
                  ))}
                  {k.scope === "global" && <Badge variant="outline">global</Badge>}
                </div>
              </TableCell>
              <TableCell className="text-sm text-muted-foreground">
                {formatTimestamp(k.lastUsedAt)}
              </TableCell>
              <TableCell className="text-right">
                <div className="flex justify-end gap-2">
                  <Button
                    size="sm"
                    variant="outline"
                    disabled={rotating || revoked}
                    onClick={() => onRotate(k.id ?? "")}
                  >
                    Rotate
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    className="text-destructive hover:text-destructive"
                    disabled={deleting || revoked}
                    onClick={() => onDelete(k.id ?? "")}
                  >
                    Revoke
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

function CreateKeyDialog({
  onCreated,
}: {
  onCreated: (result: CreateKeyResult) => void;
}) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [scope, setScope] = useState<"tenant" | "global">("tenant");
  const [perms, setPerms] = useState<Permissions>({
    read: true,
    send: false,
    manage: false,
    events: false,
  });
  const create = useCreateKey();

  const reset = (): void => {
    setName("");
    setScope("tenant");
    setPerms({ read: true, send: false, manage: false, events: false });
  };

  const submit = (e: React.FormEvent): void => {
    e.preventDefault();
    if (!name.trim()) {
      toast.error("Give the key a name.");
      return;
    }
    const body: CreateKeyRequest = {
      name: name.trim(),
      permissions: perms,
      scope,
    };
    create.mutate(body, {
      onError: (err) =>
        toast.error(isApiError(err) ? err.message : "Failed to create key"),
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
              safe — it cannot be retrieved later.
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

            <div className="space-y-2">
              <Label htmlFor="key-scope">Scope</Label>
              <Select
                value={scope}
                onValueChange={(v) => setScope(v as "tenant" | "global")}
              >
                <SelectTrigger id="key-scope" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="tenant">Tenant (your account)</SelectItem>
                  <SelectItem value="global">Global</SelectItem>
                </SelectContent>
              </Select>
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
                    checked={Boolean(perms[p])}
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

const PERMISSION_HINTS: Record<keyof Permissions, string> = {
  read: "read chats, contacts, messages",
  send: "send messages",
  manage: "manage sessions, keys, webhooks",
  events: "subscribe to the event stream",
};

function SecretDialog({
  secret,
  onClose,
}: {
  secret: CreateKeyResult | null;
  onClose: () => void;
}) {
  const value = secret?.secret ?? "";
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
