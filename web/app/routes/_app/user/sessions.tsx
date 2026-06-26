// User: my sessions — list + create. §12 user surface, scoped to the active org.
//
// Ported from v1 app/_v1-surfaces/user/sessions.list.tsx. Reshape notes:
//   - Route object -> TanStack file-based route (/user/sessions). The v1
//     `clientLoader = requireUserPanel` guard moved up to the _app/user.tsx
//     layout's server `beforeLoad` (§12), so this route just renders.
//   - react-router <Link> -> @tanstack/react-router <Link> (params object).
//   - Data + actions reuse the FROZEN gateway hooks (browser -> gateway, Bearer
//     JWT): useSessions (cursor list) + useSessionLifecycle/useCreateSession/
//     useDeleteSession. Live status is kept fresh by the NDJSON cacheBridge
//     mounted in the AppShell (session.status events).

import { useState } from "react";
import { Link } from "@tanstack/react-router";
import { PlusIcon, RefreshCwIcon } from "lucide-react";
import { createFileRoute } from "@tanstack/react-router";
import {
  useSessions,
  useSessionLifecycle,
  useCreateSession,
  useDeleteSession,
} from "~/lib/api/hooks/sessions";
import type {
  CreateSessionRequest,
  SessionAction,
  WASession,
} from "~/lib/api/types";
import { isApiError } from "~/lib/api/envelope";
import { Button } from "~/components/ui/button";
import { Input } from "~/components/ui/input";
import { Label } from "~/components/ui/label";
import { Skeleton } from "~/components/ui/skeleton";
import {
  Card,
  CardContent,
  CardFooter,
  CardHeader,
  CardTitle,
} from "~/components/ui/card";
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
import { formatTimestamp, SessionStatusBadge } from "./-components/user-ui";

export const Route = createFileRoute("/_app/user/sessions")({
  component: SessionsList,
});

const LIFECYCLE: SessionAction[] = ["start", "stop", "restart", "logout"];

function SessionsList() {
  const sessions = useSessions();
  const lifecycle = useSessionLifecycle();
  const deleteSession = useDeleteSession();

  const run = (sessionId: string, action: SessionAction): void => {
    lifecycle.mutate(
      { sessionId, action },
      {
        onError: (err) =>
          toast.error(isApiError(err) ? err.message : "Action failed"),
        onSuccess: () => toast.success(`Session ${action} requested`),
      },
    );
  };

  const remove = (sessionId: string): void => {
    if (!window.confirm("Delete this session? This cannot be undone.")) return;
    deleteSession.mutate(
      { sessionId },
      {
        onError: (err) =>
          toast.error(isApiError(err) ? err.message : "Delete failed"),
        onSuccess: () => toast.success("Session deleted"),
      },
    );
  };

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between gap-2">
        <h1 className="text-xl font-semibold">My Sessions</h1>
        <CreateSessionDialog />
      </div>

      {sessions.isLoading ? (
        <div className="grid gap-3">
          <Skeleton className="h-28 w-full" />
          <Skeleton className="h-28 w-full" />
        </div>
      ) : sessions.isError ? (
        <ErrorState
          message={
            isApiError(sessions.error)
              ? sessions.error.message
              : "Failed to load sessions"
          }
          onRetry={() => void sessions.refetch()}
        />
      ) : (
        <SessionGrid
          rows={sessions.data?.pages.flatMap((p) => p.data) ?? []}
          pending={lifecycle.isPending}
          onAction={run}
          onDelete={remove}
        />
      )}

      {sessions.hasNextPage && (
        <div className="flex justify-center">
          <Button
            variant="outline"
            disabled={sessions.isFetchingNextPage}
            onClick={() => void sessions.fetchNextPage()}
          >
            {sessions.isFetchingNextPage ? "Loading…" : "Load more"}
          </Button>
        </div>
      )}
    </div>
  );
}

function SessionGrid({
  rows,
  pending,
  onAction,
  onDelete,
}: {
  rows: WASession[];
  pending: boolean;
  onAction: (id: string, action: SessionAction) => void;
  onDelete: (id: string) => void;
}) {
  if (rows.length === 0) {
    return (
      <Card>
        <CardContent className="py-10 text-center text-sm text-muted-foreground">
          No sessions yet. Create one to pair a WhatsApp number.
        </CardContent>
      </Card>
    );
  }

  return (
    <div className="grid gap-3">
      {rows.map((s) => (
        <Card key={s.id}>
          <CardHeader className="flex-row items-start justify-between gap-2 space-y-0">
            <div className="space-y-1">
              <CardTitle className="text-base">
                <Link
                  to="/user/sessions/$sessionId"
                  params={{ sessionId: s.id }}
                  className="hover:underline"
                >
                  {s.label || s.id}
                </Link>
              </CardTitle>
              <p className="text-xs text-muted-foreground">
                {s.phoneNumber ? `+${s.phoneNumber}` : "not paired"} · created{" "}
                {formatTimestamp(s.createdAt)}
              </p>
            </div>
            <SessionStatusBadge status={s.status} />
          </CardHeader>
          <CardContent className="flex flex-wrap gap-2">
            {LIFECYCLE.map((action) => (
              <Button
                key={action}
                size="sm"
                variant="outline"
                disabled={pending}
                onClick={() => onAction(s.id, action)}
              >
                {action}
              </Button>
            ))}
          </CardContent>
          <CardFooter className="justify-between gap-2">
            <Button asChild size="sm" variant="ghost">
              <Link to="/user/sessions/$sessionId" params={{ sessionId: s.id }}>
                Open
              </Link>
            </Button>
            <Button
              size="sm"
              variant="ghost"
              className="text-destructive hover:text-destructive"
              onClick={() => onDelete(s.id)}
            >
              Delete
            </Button>
          </CardFooter>
        </Card>
      ))}
    </div>
  );
}

function CreateSessionDialog() {
  const [open, setOpen] = useState(false);
  const [label, setLabel] = useState("");
  const [start, setStart] = useState(true);
  const [autoRead, setAutoRead] = useState(false);
  const [presenceTyping, setPresenceTyping] = useState(false);
  const create = useCreateSession();

  const reset = (): void => {
    setLabel("");
    setStart(true);
    setAutoRead(false);
    setPresenceTyping(false);
  };

  const submit = (e: React.FormEvent): void => {
    e.preventDefault();
    const body: CreateSessionRequest = {
      label: label.trim() || undefined,
      start,
      autoRead,
      presenceTyping,
    };
    create.mutate(body, {
      onError: (err) =>
        toast.error(isApiError(err) ? err.message : "Failed to create session"),
      onSuccess: () => {
        toast.success("Session created");
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
          New session
        </Button>
      </DialogTrigger>
      <DialogContent>
        <form onSubmit={submit}>
          <DialogHeader>
            <DialogTitle>Create session</DialogTitle>
            <DialogDescription>
              A new WhatsApp session. After creating, pair it via QR or a phone
              pairing code from the session page.
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-4 py-4">
            <div className="space-y-2">
              <Label htmlFor="session-label">Label</Label>
              <Input
                id="session-label"
                value={label}
                onChange={(e) => setLabel(e.target.value)}
                placeholder="e.g. Support line"
                autoFocus
              />
            </div>

            <CheckboxRow
              id="session-start"
              label="Start immediately"
              description="Begin the session right after creation."
              checked={start}
              onChange={setStart}
            />
            <CheckboxRow
              id="session-autoread"
              label="Auto-read messages"
              description="Mark inbound messages as read automatically."
              checked={autoRead}
              onChange={setAutoRead}
            />
            <CheckboxRow
              id="session-typing"
              label="Presence typing"
              description="Broadcast a typing indicator while sending."
              checked={presenceTyping}
              onChange={setPresenceTyping}
            />
          </div>

          <DialogFooter>
            <DialogClose asChild>
              <Button type="button" variant="outline">
                Cancel
              </Button>
            </DialogClose>
            <Button type="submit" disabled={create.isPending}>
              {create.isPending ? "Creating…" : "Create"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function CheckboxRow({
  id,
  label,
  description,
  checked,
  onChange,
}: {
  id: string;
  label: string;
  description: string;
  checked: boolean;
  onChange: (v: boolean) => void;
}) {
  return (
    <div className="flex items-start gap-3">
      <input
        id={id}
        type="checkbox"
        className="mt-1 size-4 accent-primary"
        checked={checked}
        onChange={(e) => onChange(e.target.checked)}
      />
      <div className="space-y-0.5">
        <Label htmlFor={id} className="font-normal">
          {label}
        </Label>
        <p className="text-xs text-muted-foreground">{description}</p>
      </div>
    </div>
  );
}

function ErrorState({
  message,
  onRetry,
}: {
  message: string;
  onRetry: () => void;
}) {
  return (
    <Card>
      <CardContent className="flex flex-col items-center gap-3 py-10 text-center">
        <p className="text-sm text-destructive">{message}</p>
        <Button variant="outline" size="sm" className="gap-1.5" onClick={onRetry}>
          <RefreshCwIcon className="size-4" aria-hidden />
          Retry
        </Button>
      </CardContent>
    </Card>
  );
}
