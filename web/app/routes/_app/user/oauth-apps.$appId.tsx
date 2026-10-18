// User: "Sign in with WhatsApp" app detail. §12 user surface. oauth.md §6.2.
//
// Three tabs, kept in the URL as ?tab= so reload / share lands on the same one:
//   - settings: client credentials (copy ID, rotate secret) + the shared
//     OAuthAppForm editor. Save is enabled only when something changed; the
//     form re-seeds from the server when the app changes and isn't dirty.
//   - users: everyone who signed in (OAuth grants): WhatsApp identity, masked
//     phone, shared details, verification method, active logins; per-row
//     revoke + "sign everyone out".
//   - integration: how it works, credentials, a "Try sign-in" button (real
//     authorize URL with PKCE, opens the public consent page), and ready-to-run
//     snippets (Node, Python, authorize URL, discovery).
// Disable / enable and delete live in the page header, each behind a confirm.

import { useEffect, useMemo, useState, type ReactNode } from "react";
import {
  Link,
  createFileRoute,
  useBlocker,
  useNavigate,
} from "@tanstack/react-router";
import {
  ArrowLeftIcon,
  ExternalLinkIcon,
  KeyRoundIcon,
  PauseCircleIcon,
  PlayCircleIcon,
  RefreshCwIcon,
  Trash2Icon,
  UsersIcon,
} from "lucide-react";
import { toast } from "sonner";
import { Button } from "~/components/ui/button";
import { Skeleton } from "~/components/ui/skeleton";
import { Badge } from "~/components/ui/badge";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "~/components/ui/card";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "~/components/ui/tabs";
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
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "~/components/ui/empty";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "~/components/ui/select";
import { Page, PageTitle } from "~/components/shell/page-title";
import { useConfirm } from "~/components/confirm-dialog";
import { CodeSnippet } from "~/components/code-snippet";
import { CopyableId } from "~/components/copyable";
import { Time } from "~/components/time";
import { isApiError } from "~/lib/api/envelope";
import {
  useOAuthApp,
  useUpdateOAuthApp,
  useDeleteOAuthApp,
  useRotateOAuthAppSecret,
  useSetOAuthAppStatus,
  useOAuthAppGrants,
  useRevokeOAuthAppGrant,
  useRevokeAllOAuthAppGrants,
  type OAuthApp,
  type OAuthGrant,
  type OAuthAppPatchBody,
} from "~/lib/api/hooks/oauth";
import { DOCS } from "~/lib/docs-links";
import { titleHead } from "~/lib/head";
import { middleTruncate } from "~/lib/format";
import {
  AppAvatar,
  AppStatusBadge,
  ClientTypeBadge,
  InfoRow,
  MODE_META,
  ModeChips,
  SecretDialog,
} from "./-oauth/ui";
import {
  OAuthAppForm,
  describeErrors,
  focusFirstError,
  formFingerprint,
  formStateFromApp,
  toRequestBody,
  validateForm,
  type OAuthFormState,
} from "./-oauth/OAuthAppForm";
import { buildAuthorizeUrl, discoveryUrl, humanizeSeconds } from "./-oauth/validation";
import { discoveryCurl, nodeQuickstart, pythonQuickstart } from "./-oauth/quickstart";
import { scopeTitle } from "./-oauth/scopes";

const TABS = ["settings", "users", "integration"] as const;
type DetailTab = (typeof TABS)[number];

export const Route = createFileRoute("/_app/user/oauth-apps/$appId")({
  validateSearch: (search: Record<string, unknown>): { tab?: DetailTab } => {
    const tab = search.tab;
    return typeof tab === "string" && (TABS as readonly string[]).includes(tab)
      ? { tab: tab as DetailTab }
      : {};
  },
  head: () => titleHead("Sign in with WhatsApp"),
  component: OAuthAppDetail,
});

const ROUTER_ORIGIN = (import.meta.env.VITE_GATEWAY_URL ?? "").replace(/\/+$/, "");

function OAuthAppDetail() {
  const { appId } = Route.useParams();
  const app = useOAuthApp(appId);

  if (app.isLoading) {
    return (
      <Page>
        <div className="flex items-center gap-3">
          <Skeleton className="size-10 rounded-full" />
          <div className="grid gap-2">
            <Skeleton className="h-5 w-48" />
            <Skeleton className="h-4 w-64" />
          </div>
        </div>
        <Skeleton className="h-8 w-80" />
        <Skeleton className="h-64 w-full rounded-xl" />
      </Page>
    );
  }
  if (app.isError || !app.data) {
    return (
      <Page>
        <Empty>
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <KeyRoundIcon aria-hidden />
            </EmptyMedia>
            <EmptyTitle>App not found</EmptyTitle>
            <EmptyDescription>
              {isApiError(app.error) && app.error.status !== 404
                ? app.error.message
                : "It may have been deleted, or it belongs to another organization."}
            </EmptyDescription>
          </EmptyHeader>
          <EmptyContent>
            <Button asChild variant="outline" size="sm">
              <Link to="/user/oauth-apps">Back to sign-in apps</Link>
            </Button>
          </EmptyContent>
        </Empty>
      </Page>
    );
  }

  return <DetailView app={app.data} />;
}

function DetailView({ app }: { app: OAuthApp }) {
  const { tab = "settings" } = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const setTab = (next: string) =>
    void navigate({
      search: { tab: next === "settings" ? undefined : (next as DetailTab) },
      replace: true,
    });
  const secretFlow = useRotateSecret(app);

  return (
    <Page>
      <PageTitle
        eyebrow={
          <Link
            to="/user/oauth-apps"
            className="inline-flex items-center gap-1 transition-colors duration-150 ease-soft hover:text-foreground"
          >
            <ArrowLeftIcon className="size-3.5" aria-hidden />
            Sign in with WhatsApp
          </Link>
        }
        title={
          <>
            <AppAvatar name={app.name} logoUrl={app.logoUrl} />
            <span className="truncate">{app.name}</span>
            <AppStatusBadge status={app.status} />
          </>
        }
        description={
          <span className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1.5">
            <span className="inline-flex min-w-0 items-center gap-1.5">
              Client ID
              <CopyableId value={app.clientId} label="Copy client ID" className="text-foreground" />
            </span>
            <ClientTypeBadge type={app.clientType} />
            <ModeChips modes={app.modes} />
          </span>
        }
        actions={<HeaderActions app={app} />}
      />

      {app.status === "disabled" ? <DisabledBanner app={app} /> : null}

      <Tabs value={tab} onValueChange={setTab} className="gap-5">
        <TabsList>
          <TabsTrigger value="settings">Settings</TabsTrigger>
          <TabsTrigger value="users">Signed-in users</TabsTrigger>
          <TabsTrigger value="integration">Integration</TabsTrigger>
        </TabsList>

        <TabsContent value="settings" forceMount hidden={tab !== "settings"}>
          <SettingsTab key={app.id} app={app} secretFlow={secretFlow} />
        </TabsContent>
        <TabsContent value="users">
          <UsersTab app={app} onOpenIntegration={() => setTab("integration")} />
        </TabsContent>
        <TabsContent value="integration">
          <IntegrationTab app={app} secretFlow={secretFlow} />
        </TabsContent>
      </Tabs>

      <SecretDialog
        secret={secretFlow.secret}
        clientId={app.clientId}
        rotated
        onClose={secretFlow.clear}
      />
    </Page>
  );
}

function DisabledBanner({ app }: { app: OAuthApp }) {
  const setStatus = useSetOAuthAppStatus(app.id);
  return (
    <div className="flex flex-col gap-3 rounded-xl bg-amber-500/10 px-4 py-3 text-sm sm:flex-row sm:items-center sm:justify-between">
      <p className="text-pretty text-amber-900 dark:text-amber-200">
        <span className="font-medium">This app is paused.</span> New sign-ins
        and token refreshes are refused until you enable it again.
      </p>
      <Button
        size="sm"
        variant="outline"
        className="shrink-0"
        disabled={setStatus.isPending}
        onClick={() =>
          setStatus.mutate("enable", {
            onError: (err) => toast.error(isApiError(err) ? err.message : "Couldn't enable the app"),
            onSuccess: () => toast.success(`“${app.name}” is active again`),
          })
        }
      >
        <PlayCircleIcon aria-hidden data-icon="inline-start" />
        Enable
      </Button>
    </div>
  );
}

// --- Secret rotation (shared by Settings + Integration) ----------------------

interface RotateSecretFlow {
  secret: string | null;
  pending: boolean;
  rotate: () => void;
  clear: () => void;
}

function useRotateSecret(app: OAuthApp): RotateSecretFlow {
  const rotateMut = useRotateOAuthAppSecret(app.id);
  const confirm = useConfirm();
  const [secret, setSecret] = useState<string | null>(null);

  const rotate = async () => {
    const ok = await confirm({
      title: "Create a new client secret?",
      description:
        "The current secret stops working immediately. Update your app's server with the new secret right away, or sign-ins will fail.",
      confirmLabel: "Rotate secret",
      destructive: true,
    });
    if (!ok) return;
    rotateMut.mutate(undefined, {
      onError: (err) => toast.error(isApiError(err) ? err.message : "Couldn't rotate the secret"),
      onSuccess: (result) => {
        if (result.clientSecret) setSecret(result.clientSecret);
        toast.success("New client secret created");
      },
    });
  };

  return {
    secret,
    pending: rotateMut.isPending,
    rotate: () => void rotate(),
    clear: () => setSecret(null),
  };
}

function SecretValue({ app }: { app: OAuthApp }) {
  if (app.clientType === "public") {
    return (
      <span className="text-sm text-muted-foreground">
        None. Public apps prove themselves with PKCE instead.
      </span>
    );
  }
  return (
    <span className="font-mono text-xs text-muted-foreground">
      {app.secretLast4 ? `••••••••${app.secretLast4}` : "••••••••"}
    </span>
  );
}

function RotateButton({ app, flow }: { app: OAuthApp; flow: RotateSecretFlow }) {
  if (app.clientType === "public") return null;
  return (
    <Button variant="outline" size="sm" disabled={flow.pending} onClick={flow.rotate}>
      <RefreshCwIcon aria-hidden data-icon="inline-start" />
      {flow.pending ? "Rotating…" : "Rotate secret"}
    </Button>
  );
}

// --- Settings tab -------------------------------------------------------------

function SettingsTab({ app, secretFlow }: { app: OAuthApp; secretFlow: RotateSecretFlow }) {
  const [state, setState] = useState<OAuthFormState>(() => formStateFromApp(app));
  const [baseline, setBaseline] = useState(() => formFingerprint(formStateFromApp(app)));
  const [submitted, setSubmitted] = useState(false);
  const update = useUpdateOAuthApp(app.id);
  const confirm = useConfirm();

  const dirty = formFingerprint(state) !== baseline;

  // Re-seed from the server copy when the app changes (enable/disable, rotate,
  // a save) and there are no unsaved edits to lose.
  useEffect(() => {
    if (dirty) return;
    const fresh = formStateFromApp(app);
    setState(fresh);
    setBaseline(formFingerprint(fresh));
    // Keyed on the server version only (not on every edit).
  }, [app.updatedAt]);

  useBlocker({
    shouldBlockFn: async ({ current, next }) => {
      if (!dirty || current.pathname === next.pathname) return false;
      const leave = await confirm({
        title: "Discard unsaved changes?",
        description: `Your edits to “${app.name}” haven't been saved.`,
        confirmLabel: "Discard",
        cancelLabel: "Keep editing",
        destructive: true,
      });
      return !leave;
    },
    enableBeforeUnload: () => dirty,
  });

  const errors = validateForm(state);
  const missing = submitted ? describeErrors(errors) : "";

  const reset = () => {
    setState(formStateFromApp(app));
    setBaseline(formFingerprint(formStateFromApp(app)));
    setSubmitted(false);
  };

  const save = (e: React.FormEvent) => {
    e.preventDefault();
    setSubmitted(true);
    if (Object.keys(errors).length > 0) {
      focusFirstError("edit", errors);
      return;
    }
    // toRequestBody yields concrete (non-null) values, compatible with the
    // patch body which forbids nulls.
    update.mutate(toRequestBody(state) as OAuthAppPatchBody, {
      onError: (err) => toast.error(isApiError(err) ? err.message : "Couldn't save changes"),
      onSuccess: () => {
        setBaseline(formFingerprint(state));
        setSubmitted(false);
        toast.success("Changes saved");
      },
    });
  };

  return (
    <div className="grid gap-6">
      <Card>
        <CardHeader>
          <CardTitle>Credentials</CardTitle>
          <CardDescription>
            Your app uses these to talk to WhatsApp Gateway. The client ID is
            public; keep the secret on your server.
          </CardDescription>
        </CardHeader>
        <CardContent className="divide-y">
          <InfoRow label="Client ID">
            <CopyableId value={app.clientId} label="Copy client ID" />
          </InfoRow>
          <InfoRow
            label="Client secret"
            hint={app.clientType === "public" ? undefined : "Shown only once, when created"}
            action={<RotateButton app={app} flow={secretFlow} />}
          >
            <SecretValue app={app} />
          </InfoRow>
        </CardContent>
      </Card>

      <form onSubmit={save} noValidate className="grid gap-6">
        <OAuthAppForm state={state} onChange={setState} idPrefix="edit" showErrors={submitted} />

        <div className="flex flex-col gap-3 border-t pt-6 sm:flex-row sm:items-center sm:justify-end">
          {missing ? (
            <p className="text-sm text-destructive sm:mr-auto" role="status">
              Still needed: {missing}
            </p>
          ) : (
            <p className="text-sm text-muted-foreground sm:mr-auto" role="status">
              {dirty ? "You have unsaved changes." : "All changes saved."}
            </p>
          )}
          <Button type="button" variant="outline" disabled={!dirty} onClick={reset}>
            Discard changes
          </Button>
          <Button type="submit" disabled={update.isPending || !dirty}>
            {update.isPending ? "Saving…" : "Save changes"}
          </Button>
        </div>
      </form>
    </div>
  );
}

// --- Signed-in users tab (grants) ---------------------------------------------

function UsersTab({ app, onOpenIntegration }: { app: OAuthApp; onOpenIntegration: () => void }) {
  const grants = useOAuthAppGrants(app.id);
  const revoke = useRevokeOAuthAppGrant(app.id);
  const revokeAllMut = useRevokeAllOAuthAppGrants(app.id);
  const confirm = useConfirm();
  const rows = (grants.data?.pages.flatMap((p) => p.data) ?? []).filter((g) => !g.revokedAt);

  const revokeOne = async (grant: OAuthGrant) => {
    const who = grant.displayName || grant.phoneMasked || "this user";
    const ok = await confirm({
      title: <>Sign out {who}?</>,
      description: `They lose access to ${app.name} and have to sign in with WhatsApp again. Tokens already issued stop refreshing right away.`,
      confirmLabel: "Revoke access",
      destructive: true,
    });
    if (!ok) return;
    revoke.mutate(grant.id, {
      onError: (err) => toast.error(isApiError(err) ? err.message : "Couldn't revoke access"),
      onSuccess: () => toast.success(`Signed out ${who}`),
    });
  };

  const revokeAll = async () => {
    const ok = await confirm({
      title: `Sign out all ${rows.length} ${rows.length === 1 ? "user" : "users"}?`,
      description: `Everyone signed in to ${app.name} loses access and has to sign in with WhatsApp again. This can't be undone.`,
      confirmLabel: "Sign everyone out",
      destructive: true,
    });
    if (!ok) return;
    // Single server-side call revokes every grant + all refresh families.
    revokeAllMut.mutate(undefined, {
      onError: (err) => toast.error(isApiError(err) ? err.message : "Couldn't sign everyone out"),
      onSuccess: () => toast.success("Everyone has been signed out"),
    });
  };

  return (
    <div className="grid gap-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-sm text-pretty text-muted-foreground">
          People who signed in to {app.name} with WhatsApp. Revoking someone
          signs them out of your app.
        </p>
        {rows.length > 0 ? (
          <Button
            variant="destructive"
            size="sm"
            disabled={revokeAllMut.isPending}
            onClick={() => void revokeAll()}
          >
            {revokeAllMut.isPending ? "Signing out…" : "Sign everyone out"}
          </Button>
        ) : null}
      </div>

      <TableCard>
        {grants.isLoading ? (
          <div className="grid gap-2 p-4">
            <Skeleton className="h-10 w-full" />
            <Skeleton className="h-10 w-full" />
          </div>
        ) : grants.isError ? (
          <div className="flex flex-col items-center gap-3 py-10 text-center">
            <p className="text-sm text-destructive">
              {isApiError(grants.error) ? grants.error.message : "Couldn't load users"}
            </p>
            <Button variant="outline" size="sm" onClick={() => void grants.refetch()}>
              <RefreshCwIcon aria-hidden data-icon="inline-start" />
              Retry
            </Button>
          </div>
        ) : rows.length === 0 ? (
          <Empty className="border-0 py-12">
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <UsersIcon aria-hidden />
              </EmptyMedia>
              <EmptyTitle>No one has signed in yet</EmptyTitle>
              <EmptyDescription className="text-pretty">
                When someone signs in to your app with WhatsApp, they show up
                here. You can test it yourself from the Integration tab.
              </EmptyDescription>
            </EmptyHeader>
            <EmptyContent>
              <Button size="sm" variant="outline" onClick={onOpenIntegration}>
                Go to Integration
              </Button>
            </EmptyContent>
          </Empty>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>User</TableHead>
                <TableHead>Shared with your app</TableHead>
                <TableHead>Verified via</TableHead>
                <TableHead>Active logins</TableHead>
                <TableHead>First sign-in</TableHead>
                <TableHead>Last active</TableHead>
                <TableHead className="text-right">
                  <span className="sr-only">Actions</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((g) => (
                <TableRow key={g.id}>
                  <TableCell>
                    <div className="grid min-w-0">
                      {g.displayName ? (
                        <span className="text-sm font-medium">{g.displayName}</span>
                      ) : (
                        <CopyableId
                          value={g.sub}
                          display={middleTruncate(g.sub, 8, 6)}
                          label="Copy user ID (sub)"
                        />
                      )}
                      {g.phoneMasked ? (
                        <span className="text-xs text-muted-foreground tabular-nums">
                          {g.phoneMasked}
                        </span>
                      ) : null}
                    </div>
                  </TableCell>
                  <TableCell>
                    <div className="flex flex-wrap gap-1">
                      {(g.grantedScopes ?? []).map((s) => (
                        <Badge key={s} variant="secondary" title={s}>
                          {scopeTitle(s)}
                        </Badge>
                      ))}
                    </div>
                  </TableCell>
                  <TableCell className="text-sm">
                    {MODE_META[g.lastAcr === "wa:group" ? "group" : "dm"].label}
                  </TableCell>
                  <TableCell className="text-sm text-muted-foreground tabular-nums">
                    {g.refreshFamilyCount > 0
                      ? `${g.refreshFamilyCount} ${g.refreshFamilyCount === 1 ? "device" : "devices"}`
                      : "None"}
                  </TableCell>
                  <TableCell className="text-sm text-muted-foreground">
                    <Time value={g.createdAt} />
                  </TableCell>
                  <TableCell className="text-sm text-muted-foreground">
                    <Time value={g.lastUsedAt} fallback="Never" />
                  </TableCell>
                  <TableCell className="text-right">
                    <Button
                      size="sm"
                      variant="ghost"
                      className="text-destructive hover:bg-destructive/10 hover:text-destructive"
                      disabled={revoke.isPending}
                      onClick={() => void revokeOne(g)}
                    >
                      Revoke
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </TableCard>

      {grants.hasNextPage && (
        <div className="flex justify-center">
          <Button
            variant="outline"
            size="sm"
            disabled={grants.isFetchingNextPage}
            onClick={() => void grants.fetchNextPage()}
          >
            {grants.isFetchingNextPage ? "Loading…" : "Load more"}
          </Button>
        </div>
      )}
    </div>
  );
}

// --- Integration tab ------------------------------------------------------------

const EXAMPLE_REDIRECT = "https://app.example.com/callback";

function IntegrationTab({ app, secretFlow }: { app: OAuthApp; secretFlow: RotateSecretFlow }) {
  // The gateway resolves and returns the effective OIDC issuer on the app DTO.
  const issuer = (app.issuer || ROUTER_ORIGIN || "https://your-gateway.example.com").replace(
    /\/+$/,
    "",
  );
  const scopes = useMemo(() => app.allowedScopes ?? ["openid"], [app.allowedScopes]);
  const redirects = app.redirectUris ?? [];
  const [chosenRedirect, setChosenRedirect] = useState(redirects[0] ?? "");
  const redirectUri = redirects.includes(chosenRedirect)
    ? chosenRedirect
    : (redirects[0] ?? EXAMPLE_REDIRECT);
  const modes = app.modes ?? [];
  const bothModes = modes.includes("dm") && modes.includes("group");

  const authorizeUrl = useMemo(
    () =>
      buildAuthorizeUrl({
        issuer,
        clientId: app.clientId,
        redirectUri,
        scopes,
        acrValues: bothModes ? "wa:dm" : undefined,
      }),
    [issuer, app.clientId, redirectUri, scopes, bothModes],
  );

  const snippetTabs = useMemo(() => {
    const input = {
      issuer,
      clientId: app.clientId,
      clientType: app.clientType,
      redirectUri,
      scopes,
    };
    return [
      { id: "node", label: "Node.js", code: nodeQuickstart(input) },
      { id: "python", label: "Python", code: pythonQuickstart(input) },
      { id: "authorize", label: "Authorize URL", code: authorizeUrl },
      { id: "discovery", label: "Discovery", code: discoveryCurl(issuer) },
    ];
  }, [issuer, app.clientId, app.clientType, redirectUri, scopes, authorizeUrl]);

  return (
    <div className="grid gap-6">
      <Card>
        <CardHeader>
          <CardTitle>How it works</CardTitle>
          <CardDescription className="text-pretty">
            Sign in with WhatsApp is standard OpenID Connect (OAuth 2.0 with
            PKCE), so any OIDC library can do the heavy lifting.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <ol className="grid gap-3 text-sm sm:grid-cols-3">
            <Step n={1}>
              Your site sends the user to the WhatsApp sign-in page with your{" "}
              <span className="font-medium text-foreground">client ID</span>.
            </Step>
            <Step n={2}>
              They send a short code from their WhatsApp to your number. No
              password needed.
            </Step>
            <Step n={3}>
              They return to your{" "}
              <span className="font-medium text-foreground">redirect URI</span>{" "}
              with a one-time code. Your server swaps it for their details
              {app.clientType === "confidential" ? " using the client secret" : ""}.
            </Step>
          </ol>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Values for your app</CardTitle>
          <CardDescription>
            Paste these into your OIDC library's configuration.
          </CardDescription>
        </CardHeader>
        <CardContent className="divide-y">
          <InfoRow label="Discovery URL" hint="Your library reads every endpoint from here">
            <CopyableId value={discoveryUrl(issuer)} label="Copy discovery URL" />
          </InfoRow>
          <InfoRow label="Issuer">
            <CopyableId value={issuer} label="Copy issuer" />
          </InfoRow>
          <InfoRow label="Client ID">
            <CopyableId value={app.clientId} label="Copy client ID" />
          </InfoRow>
          <InfoRow
            label="Client secret"
            hint={
              app.clientType === "confidential"
                ? "Shown once when created. Lost it? Rotate to get a new one."
                : undefined
            }
            action={<RotateButton app={app} flow={secretFlow} />}
          >
            <SecretValue app={app} />
          </InfoRow>
          <InfoRow
            label="Redirect URI"
            hint={redirects.length > 1 ? "Used in the snippets below" : undefined}
          >
            {redirects.length > 1 ? (
              <Select value={redirectUri} onValueChange={setChosenRedirect}>
                <SelectTrigger className="w-full font-mono text-xs" aria-label="Redirect URI for the snippets">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {redirects.map((uri) => (
                    <SelectItem key={uri} value={uri} className="font-mono text-xs">
                      {uri}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            ) : redirects.length === 1 ? (
              <CopyableId value={redirectUri} label="Copy redirect URI" />
            ) : (
              <span className="text-sm text-muted-foreground">
                None yet. Add one on the Settings tab.
              </span>
            )}
          </InfoRow>
          <InfoRow label="Scopes">
            <span className="font-mono text-xs">{scopes.join(" ")}</span>
          </InfoRow>
        </CardContent>
      </Card>

      <TrySignIn app={app} authorizeUrl={authorizeUrl} redirectUri={redirects.length ? redirectUri : null} />

      <Card>
        <CardHeader>
          <CardTitle>Add it to your app</CardTitle>
          <CardDescription className="text-pretty">
            A complete login + callback example, pre-filled with this app's
            values.
            {app.clientType === "confidential"
              ? " Set WA_CLIENT_SECRET in your server's environment first."
              : ""}{" "}
            Access tokens last {humanizeSeconds(app.tokenTtlSeconds)}.
          </CardDescription>
        </CardHeader>
        <CardContent className="grid gap-3">
          <CodeSnippet tabs={snippetTabs} />
          <p className="text-xs text-pretty text-muted-foreground">
            In the authorize URL, your library fills in <code className="font-mono">state</code>{" "}
            and the PKCE <code className="font-mono">code_challenge</code>.{" "}
            <a
              href={DOCS.signInWithWhatsApp}
              target="_blank"
              rel="noreferrer"
              className="font-medium text-foreground/80 underline-offset-4 hover:text-foreground hover:underline"
            >
              Read the full guide
            </a>{" "}
            for claims, refresh tokens and group verification.
          </p>
        </CardContent>
      </Card>

    </div>
  );
}

function Step({ n, children }: { n: number; children: ReactNode }) {
  return (
    <li className="flex gap-3 text-pretty text-muted-foreground">
      <span className="flex size-6 shrink-0 items-center justify-center rounded-full bg-muted text-xs font-semibold text-foreground tabular-nums">
        {n}
      </span>
      <span>{children}</span>
    </li>
  );
}

/** Opens the real authorize URL (fresh PKCE challenge + state) in a new tab,
 * so the owner can walk through the consent page their users will see. */
function TrySignIn({
  app,
  authorizeUrl,
  redirectUri,
}: {
  app: OAuthApp;
  authorizeUrl: string;
  redirectUri: string | null;
}) {
  const canHash = typeof window === "undefined" || Boolean(window.crypto?.subtle);
  const blocked =
    app.status !== "active"
      ? "Enable the app to try it."
      : !redirectUri
        ? "Add a redirect URI on the Settings tab first."
        : !canHash
          ? "Your browser can't create a PKCE challenge on this (non-HTTPS) page."
          : null;

  const open = async () => {
    // Open synchronously (keeps the click's popup permission), then navigate.
    const win = window.open("about:blank", "_blank");
    if (win) win.opener = null;
    try {
      const url = new URL(authorizeUrl);
      url.searchParams.set("state", randomToken(16));
      url.searchParams.set("code_challenge", await pkceChallenge(randomToken(32)));
      if (win) win.location.href = url.toString();
      else window.open(url.toString(), "_blank", "noopener");
    } catch {
      win?.close();
      toast.error("Couldn't start a test sign-in");
    }
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle>Try it yourself</CardTitle>
        <CardDescription className="text-pretty">
          Opens the real sign-in page in a new tab. Send the code from your own
          WhatsApp to see the full flow. Afterwards you're sent to{" "}
          {redirectUri ? (
            <code className="font-mono text-foreground break-all">{redirectUri}</code>
          ) : (
            "your redirect URI"
          )}
          ; if your app isn't running yet, that page won't load, which is fine.
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-wrap items-center gap-3">
        <Button onClick={() => void open()} disabled={Boolean(blocked)}>
          <ExternalLinkIcon aria-hidden data-icon="inline-start" />
          Try sign-in
        </Button>
        {blocked ? <span className="text-sm text-muted-foreground">{blocked}</span> : null}
      </CardContent>
    </Card>
  );
}

function randomToken(bytes: number): string {
  const buf = new Uint8Array(bytes);
  crypto.getRandomValues(buf);
  return base64Url(buf);
}

async function pkceChallenge(verifier: string): Promise<string> {
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(verifier));
  return base64Url(new Uint8Array(digest));
}

function base64Url(buf: Uint8Array): string {
  let s = "";
  for (const b of buf) s += String.fromCharCode(b);
  return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

// --- Header actions (enable/disable + delete) -----------------------------------

function HeaderActions({ app }: { app: OAuthApp }) {
  const setStatus = useSetOAuthAppStatus(app.id);
  const del = useDeleteOAuthApp();
  const navigate = useNavigate();
  const confirm = useConfirm();
  const active = app.status === "active";

  const toggle = async () => {
    if (active) {
      const ok = await confirm({
        title: <>Pause “{app.name}”?</>,
        description: `New sign-ins and token refreshes are refused right away. People already signed in keep access until their current token expires (up to ${humanizeSeconds(app.tokenTtlSeconds)}). Enable the app again to resume; to cut off access for good, revoke users or delete the app.`,
        confirmLabel: "Disable app",
      });
      if (!ok) return;
    }
    setStatus.mutate(active ? "disable" : "enable", {
      onError: (err) => toast.error(isApiError(err) ? err.message : "Action failed"),
      onSuccess: () => toast.success(active ? `“${app.name}” is paused` : `“${app.name}” is active again`),
    });
  };

  const doDelete = async () => {
    const ok = await confirm({
      title: <>Delete “{app.name}”?</>,
      description:
        "Everyone signed in through this app is signed out, pending sign-ins stop working, and the client ID and secret are retired. This can't be undone.",
      confirmLabel: "Delete app",
      destructive: true,
      confirmText: app.name,
    });
    if (!ok) return;
    del.mutate(app.id, {
      onError: (err) => toast.error(isApiError(err) ? err.message : "Couldn't delete the app"),
      onSuccess: () => {
        toast.success(`Deleted “${app.name}”`);
        void navigate({ to: "/user/oauth-apps" });
      },
    });
  };

  return (
    <>
      <Button variant="outline" size="sm" disabled={setStatus.isPending} onClick={() => void toggle()}>
        {active ? (
          <PauseCircleIcon aria-hidden data-icon="inline-start" />
        ) : (
          <PlayCircleIcon aria-hidden data-icon="inline-start" />
        )}
        {active ? "Disable" : "Enable"}
      </Button>
      <Button variant="destructive" size="sm" disabled={del.isPending} onClick={() => void doDelete()}>
        <Trash2Icon aria-hidden data-icon="inline-start" />
        {del.isPending ? "Deleting…" : "Delete"}
      </Button>
    </>
  );
}
