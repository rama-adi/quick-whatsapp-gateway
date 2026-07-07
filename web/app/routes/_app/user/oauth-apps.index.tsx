// User: OAuth apps — list + create. §12 user surface, scoped to the active org.
// oauth.md §6.2. The INDEX of /user/oauth-apps; the detail lives in the sibling
// $appId route, rendered in the layout's Outlet.
//
// List columns: name + logo, bound session (number + live status pill), mode
// chips, grant count, status. "New app" opens a full-width Sheet with the shared
// OAuthAppForm + live consent preview. On create, the one-time client_secret is
// surfaced in the copy-once modal (confidential clients).

import { useState } from "react";
import { Link, createFileRoute } from "@tanstack/react-router";
import { PlusIcon, RefreshCwIcon, KeyRoundIcon } from "lucide-react";
import { toast } from "sonner";
import { Button } from "~/components/ui/button";
import { Skeleton } from "~/components/ui/skeleton";
import { Avatar, AvatarFallback, AvatarImage } from "~/components/ui/avatar";
import {
  Card,
  CardContent,
} from "~/components/ui/card";
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
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "~/components/ui/table";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "~/components/ui/sheet";
import { isApiError } from "~/lib/api/envelope";
import { useSessions } from "~/lib/api/hooks/sessions";
import {
  useOAuthApps,
  useCreateOAuthApp,
  type OAuthApp,
} from "~/lib/api/hooks/oauth";
import { SessionStatusBadge } from "./-components/user-ui";
import { AppStatusBadge, ModeChips, SecretDialog } from "./-oauth/ui";
import {
  OAuthAppForm,
  emptyFormState,
  isFormValid,
  toRequestBody,
  type OAuthFormState,
} from "./-oauth/OAuthAppForm";

export const Route = createFileRoute("/_app/user/oauth-apps/")({
  component: OAuthAppsList,
});

function OAuthAppsList() {
  const apps = useOAuthApps();
  const [secret, setSecret] = useState<string | null>(null);
  const rows = apps.data?.pages.flatMap((p) => p.data) ?? [];

  return (
    <div className="space-y-4">
      <div className="flex items-start justify-between gap-2">
        <div>
          <h1 className="text-xl font-semibold">Sign in with WhatsApp</h1>
          <p className="text-sm text-muted-foreground">
            OAuth applications that let third-party apps sign users in with their
            WhatsApp number.
          </p>
        </div>
        <CreateAppSheet onSecret={setSecret} />
      </div>

      {apps.isLoading ? (
        <div className="grid gap-3">
          <Skeleton className="h-14 w-full" />
          <Skeleton className="h-14 w-full" />
        </div>
      ) : apps.isError ? (
        <Card>
          <CardContent className="flex flex-col items-center gap-3 py-10 text-center">
            <p className="text-sm text-destructive">
              {isApiError(apps.error)
                ? apps.error.message
                : "Failed to load apps"}
            </p>
            <Button
              variant="outline"
              size="sm"
              className="gap-1.5"
              onClick={() => void apps.refetch()}
            >
              <RefreshCwIcon className="size-4" aria-hidden />
              Retry
            </Button>
          </CardContent>
        </Card>
      ) : rows.length === 0 ? (
        <EmptyState onSecret={setSecret} />
      ) : (
        <Card>
          <CardContent className="p-0">
            <AppTable rows={rows} />
          </CardContent>
        </Card>
      )}

      {apps.hasNextPage && (
        <div className="flex justify-center">
          <Button
            variant="outline"
            disabled={apps.isFetchingNextPage}
            onClick={() => void apps.fetchNextPage()}
          >
            {apps.isFetchingNextPage ? "Loading…" : "Load more"}
          </Button>
        </div>
      )}

      <SecretDialog secret={secret} onClose={() => setSecret(null)} />
    </div>
  );
}

function AppTable({ rows }: { rows: OAuthApp[] }) {
  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>App</TableHead>
          <TableHead>Bound session</TableHead>
          <TableHead>Modes</TableHead>
          <TableHead>Status</TableHead>
          <TableHead className="text-right">Actions</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {rows.map((app) => (
          <TableRow key={app.id}>
            <TableCell>
              <div className="flex items-center gap-3">
                <Avatar className="size-8">
                  {app.logoUrl ? <AvatarImage src={app.logoUrl} alt="" /> : null}
                  <AvatarFallback className="text-xs">
                    {initials(app.name)}
                  </AvatarFallback>
                </Avatar>
                <div className="min-w-0">
                  <Link
                    to="/user/oauth-apps/$appId"
                    params={{ appId: app.id }}
                    className="font-medium hover:underline"
                  >
                    {app.name}
                  </Link>
                  <p className="truncate font-mono text-xs text-muted-foreground">
                    {app.clientId}
                  </p>
                </div>
              </div>
            </TableCell>
            <TableCell>
              <BoundSession sessionId={app.sessionId} />
            </TableCell>
            <TableCell>
              <ModeChips modes={app.modes} />
            </TableCell>
            <TableCell>
              <AppStatusBadge status={app.status} />
            </TableCell>
            <TableCell className="text-right">
              <Button asChild size="sm" variant="ghost">
                <Link to="/user/oauth-apps/$appId" params={{ appId: app.id }}>
                  Manage
                </Link>
              </Button>
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}

/** Resolve the bound session from the already-loaded sessions list so the row
 * shows a human number + a live status pill. */
function BoundSession({ sessionId }: { sessionId: string }) {
  const sessions = useSessions();
  const rows = sessions.data?.pages.flatMap((p) => p.data) ?? [];
  const session = rows.find((s) => s.id === sessionId);
  if (!session) {
    return (
      <span className="font-mono text-xs text-muted-foreground">
        {sessionId}
      </span>
    );
  }
  return (
    <div className="flex items-center gap-2">
      <span className="text-sm">
        {session.phoneNumber ? `+${session.phoneNumber}` : session.label || session.id}
      </span>
      <SessionStatusBadge status={session.status} />
    </div>
  );
}

function EmptyState({ onSecret }: { onSecret: (s: string) => void }) {
  return (
    <Empty className="rounded-lg border border-dashed py-12">
      <EmptyHeader>
        <EmptyMedia variant="icon">
          <KeyRoundIcon />
        </EmptyMedia>
        <EmptyTitle>No OAuth apps yet</EmptyTitle>
        <EmptyDescription>
          Create an app to turn a WhatsApp session into a Sign in with WhatsApp
          identity provider. Third-party apps use it as a standard OpenID Connect
          provider.
        </EmptyDescription>
      </EmptyHeader>
      <EmptyContent>
        <CreateAppSheet onSecret={onSecret} />
      </EmptyContent>
    </Empty>
  );
}

function CreateAppSheet({ onSecret }: { onSecret: (secret: string) => void }) {
  const [open, setOpen] = useState(false);
  const [state, setState] = useState<OAuthFormState>(emptyFormState);
  const create = useCreateOAuthApp();

  const submit = () => {
    if (!isFormValid(state)) {
      toast.error("Fix the highlighted fields before creating.");
      return;
    }
    create.mutate(toRequestBody(state), {
      onError: (err) =>
        toast.error(isApiError(err) ? err.message : "Failed to create app"),
      onSuccess: (app) => {
        toast.success("App created");
        setOpen(false);
        setState(emptyFormState());
        if (app.clientSecret) onSecret(app.clientSecret);
      },
    });
  };

  return (
    <Sheet
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) setState(emptyFormState());
      }}
    >
      <Button size="sm" className="gap-1.5" onClick={() => setOpen(true)}>
        <PlusIcon className="size-4" aria-hidden />
        New app
      </Button>
      <SheetContent
        side="right"
        className="w-full gap-0 overflow-y-auto sm:max-w-3xl"
      >
        <SheetHeader>
          <SheetTitle>New OAuth app</SheetTitle>
          <SheetDescription>
            Configure the app and preview exactly what end-users will see when
            they sign in.
          </SheetDescription>
        </SheetHeader>
        <div className="px-4 py-2">
          <OAuthAppForm state={state} onChange={setState} idPrefix="create" />
        </div>
        <SheetFooter>
          <Button
            type="button"
            variant="outline"
            onClick={() => setOpen(false)}
          >
            Cancel
          </Button>
          <Button
            type="button"
            onClick={submit}
            disabled={create.isPending || !isFormValid(state)}
          >
            {create.isPending ? "Creating…" : "Create app"}
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  );
}

function initials(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean);
  if (parts.length === 0) return "?";
  if (parts.length === 1) return parts[0]!.slice(0, 2).toUpperCase();
  return (parts[0]![0]! + parts[parts.length - 1]![0]!).toUpperCase();
}
