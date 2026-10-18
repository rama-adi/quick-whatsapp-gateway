// User: "Sign in with WhatsApp" apps — the list. §12 user surface, scoped to
// the active org. oauth.md §6.2. The INDEX of /user/oauth-apps; the detail
// lives in the sibling $appId route, rendered in the layout's Outlet.
//
// Columns: name + logo + client ID, the WhatsApp number it uses (label + live
// status), verification methods, status. "New app" opens the setup page.

import { Link, createFileRoute } from "@tanstack/react-router";
import {
  ArrowRightIcon,
  LogInIcon,
  PlusIcon,
  RefreshCwIcon,
  SmartphoneIcon,
} from "lucide-react";
import { Button } from "~/components/ui/button";
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
import { Page, PageTitle } from "~/components/shell/page-title";
import { CopyableId } from "~/components/copyable";
import { SessionStatusBadge } from "~/components/status-badge";
import { isApiError } from "~/lib/api/envelope";
import { useSessions } from "~/lib/api/hooks/sessions";
import { useOAuthApps, type OAuthApp } from "~/lib/api/hooks/oauth";
import { DOCS } from "~/lib/docs-links";
import { titleHead } from "~/lib/head";
import { AppAvatar, AppStatusBadge, ModeChips } from "./-oauth/ui";
import { sessionTitle } from "~/lib/format";

export const Route = createFileRoute("/_app/user/oauth-apps/")({
  head: () => titleHead("Sign in with WhatsApp"),
  component: OAuthAppsList,
});

function OAuthAppsList() {
  const apps = useOAuthApps();
  const rows = apps.data?.pages.flatMap((p) => p.data) ?? [];
  const empty = !apps.isLoading && !apps.isError && rows.length === 0;

  return (
    <Page>
      <PageTitle
        title="Sign in with WhatsApp"
        description="Let people log in to your website or app by sending a short code from their WhatsApp to your number. No passwords."
        docsHref={DOCS.signInWithWhatsApp}
        actions={empty ? null : <NewAppButton />}
      />

      <TableCard>
        {apps.isLoading ? (
          <div className="grid gap-2 p-4">
            <Skeleton className="h-12 w-full" />
            <Skeleton className="h-12 w-full" />
          </div>
        ) : apps.isError ? (
          <div className="flex flex-col items-center gap-3 py-10 text-center">
            <p className="text-sm text-destructive">
              {isApiError(apps.error) ? apps.error.message : "Couldn't load your apps"}
            </p>
            <Button variant="outline" size="sm" onClick={() => void apps.refetch()}>
              <RefreshCwIcon aria-hidden data-icon="inline-start" />
              Retry
            </Button>
          </div>
        ) : rows.length === 0 ? (
          <EmptyState />
        ) : (
          <AppTable rows={rows} />
        )}
      </TableCard>

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
    </Page>
  );
}

function AppTable({ rows }: { rows: OAuthApp[] }) {
  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>App</TableHead>
          <TableHead>WhatsApp number</TableHead>
          <TableHead>Verified by</TableHead>
          <TableHead>Status</TableHead>
          <TableHead className="text-right">
            <span className="sr-only">Actions</span>
          </TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {rows.map((app) => (
          <TableRow key={app.id}>
            <TableCell>
              <div className="flex min-w-52 items-center gap-3">
                <AppAvatar name={app.name} logoUrl={app.logoUrl} />
                <div className="grid min-w-0">
                  <Link
                    to="/user/oauth-apps/$appId"
                    params={{ appId: app.id }}
                    className="truncate font-medium underline-offset-4 hover:underline"
                  >
                    {app.name}
                  </Link>
                  <CopyableId
                    value={app.clientId}
                    truncate
                    label="Copy client ID"
                    className="text-muted-foreground"
                  />
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
                  <ArrowRightIcon aria-hidden data-icon="inline-end" />
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
 * shows the session name, its number, and a live status pill. */
function BoundSession({ sessionId }: { sessionId: string }) {
  const sessions = useSessions();
  const rows = sessions.data?.pages.flatMap((p) => p.data) ?? [];
  const session = rows.find((s) => s.id === sessionId);
  if (!session) {
    return sessions.isLoading ? (
      <Skeleton className="h-4 w-28" />
    ) : (
      <CopyableId value={sessionId} truncate label="Copy session ID" />
    );
  }
  return (
    <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
      <div className="grid min-w-0">
        <Link
          to="/user/sessions/$sessionId"
          params={{ sessionId: session.id }}
          className="truncate text-sm underline-offset-4 hover:underline"
        >
          {sessionTitle(session)}
        </Link>
        {session.label && session.phoneNumber ? (
          <span className="text-xs text-muted-foreground tabular-nums">
            +{session.phoneNumber}
          </span>
        ) : null}
      </div>
      <SessionStatusBadge status={session.status} />
    </div>
  );
}

function EmptyState() {
  const sessions = useSessions();
  const sessionRows = sessions.data?.pages.flatMap((p) => p.data) ?? [];
  const hasConnected = sessionRows.some((s) => s.status === "working");
  const needsNumber = !sessions.isLoading && !hasConnected;

  return (
    <Empty className="border-0 py-12">
      <EmptyHeader>
        <EmptyMedia variant="icon">
          {needsNumber ? <SmartphoneIcon aria-hidden /> : <LogInIcon aria-hidden />}
        </EmptyMedia>
        <EmptyTitle>
          {needsNumber ? "Connect a WhatsApp number first" : "No sign-in apps yet"}
        </EmptyTitle>
        <EmptyDescription className="text-pretty">
          {needsNumber
            ? "Your users sign in by messaging one of your numbers. Connect a number, then come back to create a sign-in app."
            : "Create an app to add a “Sign in with WhatsApp” button to your site. It works with any standard OpenID Connect (OIDC) library."}
        </EmptyDescription>
      </EmptyHeader>
      <EmptyContent>
        {needsNumber ? (
          <div className="flex flex-wrap justify-center gap-2">
            <Button asChild size="sm">
              <Link to="/user/sessions">
                Connect a number
                <ArrowRightIcon aria-hidden data-icon="inline-end" />
              </Link>
            </Button>
            <Button asChild size="sm" variant="ghost">
              <Link to="/user/oauth-apps/new">Set up an app anyway</Link>
            </Button>
          </div>
        ) : (
          <NewAppButton />
        )}
      </EmptyContent>
    </Empty>
  );
}

function NewAppButton() {
  return (
    <Button asChild>
      <Link to="/user/oauth-apps/new">
        <PlusIcon aria-hidden data-icon="inline-start" />
        New app
      </Link>
    </Button>
  );
}
