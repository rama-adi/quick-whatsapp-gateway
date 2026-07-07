// User: OAuth app detail. §12 user surface. oauth.md §6.2.
//
// Three tabs:
//   - Settings: the shared OAuthAppForm (edit) + save; rotate-secret / secret
//     status; enable/disable; delete (destructive, explains the grant cascade).
//   - Grants: WhatsApp identity, masked phone, scopes, acr, first/last login;
//     per-row revoke + "revoke all".
//   - Integration: generated relying-app quickstart (discovery URL, client_id,
//     filled-in authorize URL, runnable Node openid-client snippet).

import { useMemo, useState } from "react";
import {
  Link,
  createFileRoute,
  useNavigate,
} from "@tanstack/react-router";
import {
  ArrowLeftIcon,
  RefreshCwIcon,
  KeyRoundIcon,
  Trash2Icon,
  PowerIcon,
  BookOpenIcon,
} from "lucide-react";
import { toast } from "sonner";
import { Button } from "~/components/ui/button";
import { Skeleton } from "~/components/ui/skeleton";
import { Badge } from "~/components/ui/badge";
import { Separator } from "~/components/ui/separator";
import { Avatar, AvatarFallback, AvatarImage } from "~/components/ui/avatar";
import { Card, CardContent, CardHeader, CardTitle } from "~/components/ui/card";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "~/components/ui/tabs";
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
} from "~/components/ui/dialog";
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
import { formatTimestamp, CopyButton } from "./-components/user-ui";
import {
  AppStatusBadge,
  ClientTypeBadge,
  ModeChips,
  SecretDialog,
} from "./-oauth/ui";
import {
  OAuthAppForm,
  formStateFromApp,
  isFormValid,
  toRequestBody,
  type OAuthFormState,
} from "./-oauth/OAuthAppForm";
import {
  buildAuthorizeUrl,
  discoveryUrl,
} from "./-oauth/validation";
import { nodeQuickstart } from "./-oauth/quickstart";

export const Route = createFileRoute("/_app/user/oauth-apps/$appId")({
  component: OAuthAppDetail,
});

const ROUTER_ORIGIN = (import.meta.env.VITE_GATEWAY_URL ?? "").replace(
  /\/+$/,
  "",
);

function OAuthAppDetail() {
  const { appId } = Route.useParams();
  const app = useOAuthApp(appId);

  if (app.isLoading) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-8 w-48" />
        <Skeleton className="h-64 w-full" />
      </div>
    );
  }
  if (app.isError || !app.data) {
    return (
      <Card>
        <CardContent className="flex flex-col items-center gap-3 py-10 text-center">
          <p className="text-sm text-destructive">
            {isApiError(app.error) ? app.error.message : "App not found"}
          </p>
          <Button asChild variant="outline" size="sm">
            <Link to="/user/oauth-apps">Back to apps</Link>
          </Button>
        </CardContent>
      </Card>
    );
  }

  return <DetailView app={app.data} />;
}

function DetailView({ app }: { app: OAuthApp }) {
  return (
    <div className="space-y-6">
      <div>
        <Button asChild variant="ghost" size="sm" className="mb-2 -ml-2 gap-1.5">
          <Link to="/user/oauth-apps">
            <ArrowLeftIcon className="size-4" aria-hidden />
            All apps
          </Link>
        </Button>
        <div className="flex items-start justify-between gap-3">
          <div className="flex items-center gap-3">
            <Avatar size="lg">
              {app.logoUrl ? <AvatarImage src={app.logoUrl} alt="" /> : null}
              <AvatarFallback>{initials(app.name)}</AvatarFallback>
            </Avatar>
            <div>
              <h1 className="text-xl font-semibold">{app.name}</h1>
              <div className="mt-1 flex flex-wrap items-center gap-2">
                <AppStatusBadge status={app.status} />
                <ClientTypeBadge type={app.clientType} />
                <ModeChips modes={app.modes} />
              </div>
            </div>
          </div>
          <DangerActions app={app} />
        </div>
      </div>

      <Tabs defaultValue="settings">
        <TabsList>
          <TabsTrigger value="settings">Settings</TabsTrigger>
          <TabsTrigger value="grants">Grants</TabsTrigger>
          <TabsTrigger value="integration">Integration</TabsTrigger>
        </TabsList>

        <TabsContent value="settings" className="pt-4">
          <SettingsTab app={app} />
        </TabsContent>
        <TabsContent value="grants" className="pt-4">
          <GrantsTab app={app} />
        </TabsContent>
        <TabsContent value="integration" className="pt-4">
          <IntegrationTab app={app} />
        </TabsContent>
      </Tabs>
    </div>
  );
}

// --- Settings tab -----------------------------------------------------------

function SettingsTab({ app }: { app: OAuthApp }) {
  const [state, setState] = useState<OAuthFormState>(() =>
    formStateFromApp(app),
  );
  const update = useUpdateOAuthApp(app.id);

  const save = () => {
    if (!isFormValid(state)) {
      toast.error("Fix the highlighted fields before saving.");
      return;
    }
    // toRequestBody yields concrete (non-null) values, compatible with the
    // patch body which forbids nulls.
    update.mutate(toRequestBody(state) as OAuthAppPatchBody, {
      onError: (err) =>
        toast.error(isApiError(err) ? err.message : "Failed to save"),
      onSuccess: () => toast.success("App updated"),
    });
  };

  return (
    <div className="space-y-6">
      <SecretSection app={app} />

      <Separator />

      <OAuthAppForm state={state} onChange={setState} idPrefix="edit" />

      <div className="flex justify-end gap-2">
        <Button
          variant="outline"
          onClick={() => setState(formStateFromApp(app))}
        >
          Reset
        </Button>
        <Button
          onClick={save}
          disabled={update.isPending || !isFormValid(state)}
        >
          {update.isPending ? "Saving…" : "Save changes"}
        </Button>
      </div>
    </div>
  );
}

function SecretSection({ app }: { app: OAuthApp }) {
  const rotate = useRotateOAuthAppSecret(app.id);
  const [secret, setSecret] = useState<string | null>(null);

  if (app.clientType === "public") {
    return (
      <Card>
        <CardHeader className="pb-2">
          <CardTitle className="text-base">Client credentials</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          <CredRow label="Client ID" value={app.clientId} />
          <div className="flex items-start gap-2 rounded-md border bg-muted/30 p-3 text-sm text-muted-foreground">
            <KeyRoundIcon className="mt-0.5 size-4 shrink-0" aria-hidden />
            <span>
              This is a <span className="font-medium text-foreground">public</span>{" "}
              client — no secret is issued. The app must use PKCE (S256), which is
              mandatory for every client.
            </span>
          </div>
        </CardContent>
      </Card>
    );
  }

  const doRotate = () => {
    if (
      !window.confirm(
        "Rotate the client secret? The current secret stops working immediately and any app using it must be updated.",
      )
    )
      return;
    rotate.mutate(undefined, {
      onError: (err) =>
        toast.error(isApiError(err) ? err.message : "Rotation failed"),
      onSuccess: (result) => {
        if (result.clientSecret) setSecret(result.clientSecret);
        toast.success("Secret rotated");
      },
    });
  };

  return (
    <Card>
      <CardHeader className="pb-2">
        <CardTitle className="text-base">Client credentials</CardTitle>
      </CardHeader>
      <CardContent className="space-y-3">
        <CredRow label="Client ID" value={app.clientId} />
        <div className="flex items-center justify-between gap-3 rounded-md border p-3">
          <div className="text-sm">
            <span className="text-muted-foreground">Client secret</span>
            <p className="font-mono">
              {app.secretLast4 ? `••••••••${app.secretLast4}` : "—"}
            </p>
          </div>
          <Button
            variant="outline"
            size="sm"
            className="gap-1.5"
            disabled={rotate.isPending}
            onClick={doRotate}
          >
            <RefreshCwIcon className="size-4" aria-hidden />
            {rotate.isPending ? "Rotating…" : "Rotate secret"}
          </Button>
        </div>
      </CardContent>
      <SecretDialog secret={secret} rotated onClose={() => setSecret(null)} />
    </Card>
  );
}

function CredRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-center justify-between gap-3 rounded-md border p-3">
      <div className="min-w-0 text-sm">
        <span className="text-muted-foreground">{label}</span>
        <p className="truncate font-mono">{value}</p>
      </div>
      <CopyButton value={value} />
    </div>
  );
}

// --- Grants tab -------------------------------------------------------------

function GrantsTab({ app }: { app: OAuthApp }) {
  const grants = useOAuthAppGrants(app.id);
  const revoke = useRevokeOAuthAppGrant(app.id);
  const revokeAllMut = useRevokeAllOAuthAppGrants(app.id);
  const rows = (grants.data?.pages.flatMap((p) => p.data) ?? []).filter(
    (g) => !g.revokedAt,
  );

  const revokeOne = (grant: OAuthGrant) => {
    if (
      !window.confirm(
        "Revoke this grant? The user's refresh tokens are revoked and they'll need to sign in again.",
      )
    )
      return;
    revoke.mutate(grant.id, {
      onError: (err) =>
        toast.error(isApiError(err) ? err.message : "Revoke failed"),
      onSuccess: () => toast.success("Grant revoked"),
    });
  };

  const revokeAll = () => {
    if (
      !window.confirm(
        `Revoke all ${rows.length} grants? Every user of this app will need to sign in again.`,
      )
    )
      return;
    // Single server-side call revokes every grant + all refresh families under
    // them (the old client-side loop over single revokes is gone).
    revokeAllMut.mutate(undefined, {
      onError: (err) =>
        toast.error(isApiError(err) ? err.message : "Revoke all failed"),
      onSuccess: () => toast.success("All grants revoked"),
    });
  };

  if (grants.isLoading) {
    return <Skeleton className="h-40 w-full" />;
  }
  if (grants.isError) {
    return (
      <Card>
        <CardContent className="flex flex-col items-center gap-3 py-10 text-center">
          <p className="text-sm text-destructive">
            {isApiError(grants.error)
              ? grants.error.message
              : "Failed to load grants"}
          </p>
          <Button
            variant="outline"
            size="sm"
            onClick={() => void grants.refetch()}
          >
            Retry
          </Button>
        </CardContent>
      </Card>
    );
  }

  if (rows.length === 0) {
    return (
      <Card>
        <CardContent className="py-10 text-center text-sm text-muted-foreground">
          No active grants yet. When someone signs in with this app, their
          consent shows up here.
        </CardContent>
      </Card>
    );
  }

  return (
    <div className="space-y-3">
      <div className="flex justify-end">
        <Button
          variant="outline"
          size="sm"
          className="text-destructive hover:text-destructive"
          disabled={revokeAllMut.isPending}
          onClick={revokeAll}
        >
          {revokeAllMut.isPending ? "Revoking…" : "Revoke all"}
        </Button>
      </div>
      <Card>
        <CardContent className="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>User</TableHead>
                <TableHead>Scopes</TableHead>
                <TableHead>Method</TableHead>
                <TableHead>Sessions</TableHead>
                <TableHead>First login</TableHead>
                <TableHead>Last login</TableHead>
                <TableHead className="text-right">Actions</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((g) => (
                <TableRow key={g.id}>
                  <TableCell>
                    <p className="text-sm font-medium">
                      {g.displayName || (
                        <span className="font-mono text-xs text-muted-foreground">
                          {truncateMiddle(g.sub, 14)}
                        </span>
                      )}
                    </p>
                    {g.phoneMasked ? (
                      <p className="font-mono text-xs text-muted-foreground">
                        {g.phoneMasked}
                      </p>
                    ) : null}
                  </TableCell>
                  <TableCell>
                    <div className="flex flex-wrap gap-1">
                      {(g.grantedScopes ?? []).map((s) => (
                        <Badge key={s} variant="secondary" className="text-[10px]">
                          {s}
                        </Badge>
                      ))}
                    </div>
                  </TableCell>
                  <TableCell>
                    <Badge variant="outline">{acrLabel(g.lastAcr)}</Badge>
                  </TableCell>
                  <TableCell className="text-sm text-muted-foreground">
                    {refreshFamilyLabel(g.refreshFamilyCount)}
                  </TableCell>
                  <TableCell className="text-sm text-muted-foreground">
                    {formatTimestamp(g.createdAt)}
                  </TableCell>
                  <TableCell className="text-sm text-muted-foreground">
                    {formatTimestamp(g.lastUsedAt)}
                  </TableCell>
                  <TableCell className="text-right">
                    <Button
                      size="sm"
                      variant="ghost"
                      className="text-destructive hover:text-destructive"
                      disabled={revoke.isPending}
                      onClick={() => revokeOne(g)}
                    >
                      Revoke
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </CardContent>
      </Card>
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

// --- Integration tab --------------------------------------------------------

function IntegrationTab({ app }: { app: OAuthApp }) {
  // The gateway resolves and returns the effective OIDC issuer on the app DTO —
  // use it verbatim (no longer derived client-side from VITE_GATEWAY_URL).
  const issuer = (app.issuer || ROUTER_ORIGIN || "https://your-gateway.example.com").replace(
    /\/+$/,
    "",
  );
  const scopes = app.allowedScopes ?? ["openid"];
  const firstRedirect = app.redirectUris?.[0] ?? "https://app.example.com/callback";
  const modes = app.modes ?? [];
  const bothModes = modes.includes("dm") && modes.includes("group");

  const authorizeUrl = useMemo(
    () =>
      buildAuthorizeUrl({
        issuer,
        clientId: app.clientId,
        redirectUri: firstRedirect,
        scopes,
        acrValues: bothModes ? "wa:dm" : undefined,
      }),
    [issuer, app.clientId, firstRedirect, scopes, bothModes],
  );

  const snippet = useMemo(
    () =>
      nodeQuickstart({
        issuer,
        clientId: app.clientId,
        clientType: app.clientType,
        redirectUri: firstRedirect,
        scopes,
      }),
    [issuer, app.clientId, app.clientType, firstRedirect, scopes],
  );

  return (
    <div className="space-y-4">
      <div className="flex items-start gap-2 rounded-lg border bg-muted/30 p-3 text-sm text-muted-foreground">
        <BookOpenIcon className="mt-0.5 size-4 shrink-0" aria-hidden />
        <span>
          Drop these values into any standard OpenID Connect client. We support
          the authorization-code flow with mandatory PKCE (S256).
        </span>
      </div>

      <Card>
        <CardHeader className="pb-2">
          <CardTitle className="text-base">1. Provider configuration</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          <CredRow label="Discovery URL" value={discoveryUrl(issuer)} />
          <CredRow label="Client ID" value={app.clientId} />
          {app.clientType === "confidential" ? (
            <p className="text-xs text-muted-foreground">
              Use the client secret shown once on the Settings tab (rotate it if
              you've lost it).
            </p>
          ) : (
            <p className="text-xs text-muted-foreground">
              Public client — no secret; your library performs PKCE automatically.
            </p>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader className="pb-2">
          <CardTitle className="text-base">2. Authorize URL</CardTitle>
        </CardHeader>
        <CardContent className="space-y-2">
          <CodeBlock value={authorizeUrl} />
          <p className="text-xs text-muted-foreground">
            Your client library fills in <code>state</code> and the PKCE{" "}
            <code>code_challenge</code>. Redirects go only to your registered
            redirect URIs.
          </p>
        </CardContent>
      </Card>

      <Card>
        <CardHeader className="pb-2">
          <CardTitle className="text-base">
            3. Node quickstart (openid-client)
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-2">
          <CodeBlock value={snippet} multiline />
        </CardContent>
      </Card>
    </div>
  );
}

function CodeBlock({
  value,
  multiline,
}: {
  value: string;
  multiline?: boolean;
}) {
  return (
    <div className="relative">
      <pre
        className={
          multiline
            ? "max-h-[28rem] overflow-auto rounded-md border bg-muted/40 p-3 text-xs leading-relaxed"
            : "overflow-x-auto rounded-md border bg-muted/40 p-3 text-xs"
        }
      >
        <code className="font-mono">{value}</code>
      </pre>
      <div className="absolute right-2 top-2">
        <CopyButton value={value} />
      </div>
    </div>
  );
}

// --- Danger actions (enable/disable + delete) -------------------------------

function DangerActions({ app }: { app: OAuthApp }) {
  const setStatus = useSetOAuthAppStatus(app.id);
  const del = useDeleteOAuthApp();
  const navigate = useNavigate();
  const [confirmDelete, setConfirmDelete] = useState(false);

  const toggle = () => {
    const action = app.status === "active" ? "disable" : "enable";
    setStatus.mutate(action, {
      onError: (err) =>
        toast.error(isApiError(err) ? err.message : "Action failed"),
      onSuccess: () =>
        toast.success(app.status === "active" ? "App disabled" : "App enabled"),
    });
  };

  const doDelete = () => {
    del.mutate(app.id, {
      onError: (err) =>
        toast.error(isApiError(err) ? err.message : "Delete failed"),
      onSuccess: () => {
        toast.success("App deleted");
        void navigate({ to: "/user/oauth-apps" });
      },
    });
  };

  return (
    <div className="flex items-center gap-2">
      <Button
        variant="outline"
        size="sm"
        className="gap-1.5"
        disabled={setStatus.isPending}
        onClick={toggle}
      >
        <PowerIcon className="size-4" aria-hidden />
        {app.status === "active" ? "Disable" : "Enable"}
      </Button>
      <Button
        variant="outline"
        size="sm"
        className="gap-1.5 text-destructive hover:text-destructive"
        onClick={() => setConfirmDelete(true)}
      >
        <Trash2Icon className="size-4" aria-hidden />
        Delete
      </Button>

      <Dialog open={confirmDelete} onOpenChange={setConfirmDelete}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete {app.name}?</DialogTitle>
            <DialogDescription>
              This permanently removes the app. Every grant is revoked and all
              refresh tokens are invalidated — everyone signed in through this app
              is signed out and outstanding authorization codes stop working.
              This cannot be undone.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <DialogClose asChild>
              <Button variant="outline">Cancel</Button>
            </DialogClose>
            <Button
              variant="destructive"
              disabled={del.isPending}
              onClick={doDelete}
            >
              {del.isPending ? "Deleting…" : "Delete app"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

// --- helpers ----------------------------------------------------------------

function acrLabel(acr: OAuthGrant["lastAcr"]): string {
  return acr === "wa:group" ? "Group" : "DM";
}

/** "N active refresh families" — live refresh-token families under a grant (each
 * family is one logged-in relying-app session). */
function refreshFamilyLabel(count: number): string {
  return `${count} active ${count === 1 ? "family" : "families"}`;
}

function truncateMiddle(s: string, keep: number): string {
  if (s.length <= keep) return s;
  const half = Math.floor(keep / 2);
  return `${s.slice(0, half)}…${s.slice(-half)}`;
}

function initials(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean);
  if (parts.length === 0) return "?";
  if (parts.length === 1) return parts[0]!.slice(0, 2).toUpperCase();
  return (parts[0]![0]! + parts[parts.length - 1]![0]!).toUpperCase();
}
