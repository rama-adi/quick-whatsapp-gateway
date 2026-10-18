// Admin: link the platform's admin WhatsApp number by QR code or pairing code.
// Guard from parent /admin.
//
// Data: admin sessions via GET /admin/sessions (useAdminSessions), filtered to
// isAdminSession. Lifecycle / QR / pairing code use the shared session hooks
// (Bearer JWT through the API client). The QR string from GET /qr (refreshed
// live by the auth.qr event) is rendered client-side by <QrImage>; the route
// returns JSON, so a plain <img src> can't work.

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { CheckCircle2Icon, QrCodeIcon, RefreshCwIcon, SmartphoneIcon } from "lucide-react";
import { qk } from "~/lib/query";
import { StreamIndicator, useLiveSessionStatus, withLiveStatus, OrgLabel, useOrgNames } from "./-shared";
import { useAdminSessions } from "~/lib/api/hooks/admin";
import { useEventStreamSubscription } from "~/lib/events/useEventStream";
import { useSessionQR, useSessionLifecycle, usePairingCode } from "~/lib/api/hooks/sessions";
import type { SessionAction, SessionStatus, WASession } from "~/lib/api/types";
import { isApiError } from "~/lib/api/envelope";
import { Button } from "~/components/ui/button";
import { Input } from "~/components/ui/input";
import { Label } from "~/components/ui/label";
import { Skeleton } from "~/components/ui/skeleton";
import { Card, CardContent, CardHeader, CardTitle } from "~/components/ui/card";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "~/components/ui/empty";
import { Page, PageTitle } from "~/components/shell/page-title";
import { useConfirm } from "~/components/confirm-dialog";
import { CopyButton, CopyableId } from "~/components/copyable";
import { SessionStatusBadge } from "~/components/status-badge";
import { DOCS } from "~/lib/docs-links";
import { titleHead } from "~/lib/head";
import { cn } from "~/lib/utils";
import { toast } from "sonner";
import { QrImage } from "~/routes/_app/user/-components/user-ui";

export const Route = createFileRoute("/_app/admin/pairing")({
  head: () => titleHead("Number pairing"),
  component: AdminPairing,
});

/** "+62 812-3456-789" → "628123456789". */
function digitsOnly(input: string): string {
  return input.replace(/[^\d]/g, "");
}

function phoneLabel(digits: string | undefined): string | undefined {
  const d = digits ? digitsOnly(digits) : "";
  return d ? `+${d}` : undefined;
}

function AdminPairing() {
  // Keep the shared event stream open: auth.qr / auth.code / status events
  // refresh the QR, the pairing code and the badge live.
  useEventStreamSubscription();
  const q = useAdminSessions();
  const overrides = useLiveSessionStatus();
  const orgNames = useOrgNames();
  const [selectedId, setSelectedId] = useState<string | null>(null);

  const adminSessions = useMemo<WASession[]>(
    () => (q.data?.pages.flatMap((p) => p.data ?? []) ?? []).filter((s) => s.isAdminSession),
    [q.data],
  );

  // Default selection to the first admin session once loaded.
  useEffect(() => {
    if (!selectedId && adminSessions.length > 0) {
      setSelectedId(adminSessions[0]?.id ?? null);
    }
  }, [adminSessions, selectedId]);

  const selected = adminSessions.find((s) => s.id === selectedId) ?? null;

  return (
    <Page width="4xl">
      <PageTitle
        title="Number pairing"
        description="Link the platform's admin WhatsApp number with a QR code or a pairing code."
        docsHref={DOCS.connectNumber}
        actions={<StreamIndicator />}
      />

      {q.isLoading ? (
        <Skeleton className="h-80 w-full rounded-xl" />
      ) : q.isError ? (
        <Card>
          <CardContent className="flex flex-col items-center gap-3 py-6 text-center">
            <p className="text-sm text-destructive">
              {isApiError(q.error) ? q.error.message : "Failed to load admin sessions"}
            </p>
            <Button variant="outline" size="sm" onClick={() => void q.refetch()}>
              <RefreshCwIcon aria-hidden />
              Retry
            </Button>
          </CardContent>
        </Card>
      ) : adminSessions.length === 0 ? (
        <Empty>
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <SmartphoneIcon aria-hidden />
            </EmptyMedia>
            <EmptyTitle>No admin number set up</EmptyTitle>
            <EmptyDescription>
              The admin number is a WhatsApp session your deployment marks as the
              platform's admin session. Once it exists, you can link it here
              without console access. See “Pairing a WhatsApp number” in the
              operator deployment guide.
            </EmptyDescription>
          </EmptyHeader>
        </Empty>
      ) : (
        <div className="grid gap-4">
          {adminSessions.length > 1 ? (
            <SessionPicker
              sessions={adminSessions}
              overrides={overrides}
              orgNames={orgNames}
              selectedId={selectedId}
              onSelect={setSelectedId}
            />
          ) : null}
          {selected ? (
            <PairingPanel
              key={selected.id}
              session={selected}
              liveStatus={withLiveStatus(selected, overrides)}
              orgNames={orgNames}
            />
          ) : null}
        </div>
      )}
    </Page>
  );
}

function SessionPicker({
  sessions,
  overrides,
  orgNames,
  selectedId,
  onSelect,
}: {
  sessions: WASession[];
  overrides: Record<string, SessionStatus>;
  orgNames: Map<string, string>;
  selectedId: string | null;
  onSelect: (id: string) => void;
}) {
  return (
    <div role="radiogroup" aria-label="Admin numbers" className="grid gap-2 sm:grid-cols-2">
      {sessions.map((s) => (
        <button
          key={s.id}
          type="button"
          role="radio"
          aria-checked={selectedId === s.id}
          onClick={() => onSelect(s.id)}
          className={cn(
            "flex items-center justify-between gap-3 rounded-xl border bg-card px-4 py-3 text-left text-sm transition-colors duration-150 ease-soft hover:bg-muted/50",
            selectedId === s.id && "border-ring/60 ring-3 ring-ring/15",
          )}
        >
          <span className="min-w-0">
            <span className="block truncate font-medium">{s.label || phoneLabel(s.phoneNumber) || "Admin number"}</span>
            <span className="block truncate text-xs text-muted-foreground">
              <OrgLabel id={s.organizationId} names={orgNames} />
            </span>
          </span>
          <SessionStatusBadge status={withLiveStatus(s, overrides)} />
        </button>
      ))}
    </div>
  );
}

const ACTION_LABEL: Record<SessionAction, string> = {
  start: "Start",
  stop: "Stop",
  restart: "Restart",
  logout: "Log out",
};

/** The lifecycle actions that make sense in this status. */
function actionsFor(status: SessionStatus | undefined, paired: boolean): SessionAction[] {
  switch (status) {
    case "working":
      return ["restart", "stop", "logout"];
    case "starting":
    case "scan_qr_code":
      return paired ? ["restart", "stop", "logout"] : ["restart", "stop"];
    case "failed":
      return paired ? ["restart", "stop", "logout"] : ["restart", "stop"];
    case "stopped":
    case "logged_out":
    default:
      return paired && status !== "logged_out" ? ["start", "logout"] : ["start"];
  }
}

function PairingPanel({
  session,
  liveStatus,
  orgNames,
}: {
  session: WASession;
  liveStatus?: SessionStatus;
  orgNames: Map<string, string>;
}) {
  const lifecycle = useSessionLifecycle();
  const confirm = useConfirm();
  const [pending, setPending] = useState<SessionAction | null>(null);
  const needsScan = liveStatus === "scan_qr_code";
  const connected = liveStatus === "working";
  const paired = connected || Boolean(session.waJid);
  const phone = phoneLabel(session.phoneNumber);

  const runAction = async (action: SessionAction): Promise<void> => {
    if (action === "logout") {
      const ok = await confirm({
        title: "Log out the admin number?",
        description: `WhatsApp unlinks this device from ${phone ?? "the phone"}. To use it again you'll need to pair it with a new QR or pairing code.`,
        confirmLabel: "Log out",
        destructive: true,
      });
      if (!ok) return;
    }
    setPending(action);
    lifecycle.mutate(
      { sessionId: session.id, action },
      {
        onSuccess: () =>
          toast.success(
            action === "logout" ? "Logged out" : action === "stop" ? "Stopped" : action === "start" ? "Starting…" : "Restarting…",
          ),
        onError: (err) => toast.error(isApiError(err) ? err.message : "Action failed"),
        onSettled: () => setPending(null),
      },
    );
  };

  return (
    <Card className="gap-0 py-0">
      <CardHeader className="flex flex-col gap-3 border-b py-4 sm:flex-row sm:items-center sm:justify-between">
        <div className="grid min-w-0 gap-1">
          <CardTitle className="flex flex-wrap items-center gap-2 text-base">
            <span className="truncate">{session.label || "Admin number"}</span>
            <SessionStatusBadge status={liveStatus} />
          </CardTitle>
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm text-muted-foreground">
            <CopyableId value={session.id} truncate label="Copy session ID" />
            <OrgLabel id={session.organizationId} names={orgNames} />
          </div>
        </div>
        <div className="flex flex-wrap gap-2">
          {actionsFor(liveStatus, paired).map((action) => (
            <Button
              key={action}
              size="sm"
              variant={action === "logout" ? "destructive" : "outline"}
              disabled={lifecycle.isPending}
              onClick={() => void runAction(action)}
            >
              {pending === action ? `${ACTION_LABEL[action]}…` : ACTION_LABEL[action]}
            </Button>
          ))}
        </div>
      </CardHeader>

      <CardContent className="p-6">
        {connected ? (
          <div className="flex flex-col items-center gap-3 py-6 text-center">
            <span className="flex size-12 items-center justify-center rounded-full bg-emerald-500/12 text-emerald-600 dark:text-emerald-400">
              <CheckCircle2Icon className="size-6" aria-hidden />
            </span>
            <div className="grid gap-1">
              <p className="font-medium">Admin number connected{phone ? ` as ${phone}` : ""}</p>
              <p className="text-sm text-muted-foreground">
                It's linked and live. There's nothing else to do here.
              </p>
            </div>
          </div>
        ) : (
          <div className="grid gap-8 md:grid-cols-2 md:gap-0 md:divide-x">
            <div className="md:pr-8">
              <QrPanel
                sessionId={session.id}
                needsScan={needsScan}
                canStart={actionsFor(liveStatus, paired).includes("start")}
                starting={pending === "start" || liveStatus === "starting"}
                onStart={() => void runAction("start")}
              />
            </div>
            <div className="md:pl-8">
              <PairingCodePanel sessionId={session.id} adminPhone={session.phoneNumber} />
            </div>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function PanelHeading({ icon, title, text }: { icon: React.ReactNode; title: string; text: string }) {
  return (
    <div className="mb-4 flex items-start gap-3">
      <span className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-muted text-muted-foreground [&_svg]:size-4">
        {icon}
      </span>
      <div>
        <h3 className="text-sm font-medium">{title}</h3>
        <p className="text-sm text-muted-foreground">{text}</p>
      </div>
    </div>
  );
}

function QrPanel({
  sessionId,
  needsScan,
  canStart,
  starting,
  onStart,
}: {
  sessionId: string;
  needsScan: boolean;
  canStart: boolean;
  starting: boolean;
  onStart: () => void;
}) {
  const qr = useSessionQR(sessionId, needsScan);
  const code = qr.data?.code;

  return (
    <section>
      <PanelHeading icon={<QrCodeIcon aria-hidden />} title="Scan a QR code" text="Best when you have the phone in front of you." />
      {!needsScan ? (
        <div className="flex flex-col items-center gap-3 rounded-xl border border-dashed px-4 py-8 text-center">
          <p className="text-sm text-muted-foreground">
            {starting
              ? "Connecting to WhatsApp… the QR code appears here in a moment."
              : "The QR code appears once the session is waiting for a scan."}
          </p>
          {canStart && !starting ? (
            <Button size="sm" onClick={onStart}>
              Start session
            </Button>
          ) : null}
        </div>
      ) : qr.isLoading ? (
        <Skeleton className="mx-auto size-56 rounded-xl" />
      ) : qr.isError ? (
        <div className="flex flex-col items-center gap-3 py-6 text-center">
          <p className="text-sm text-destructive">{isApiError(qr.error) ? qr.error.message : "QR code unavailable"}</p>
          <Button variant="outline" size="sm" onClick={() => void qr.refetch()}>
            <RefreshCwIcon aria-hidden />
            Retry
          </Button>
        </div>
      ) : (
        <QrImage code={code} />
      )}
    </section>
  );
}

function PairingCodePanel({ sessionId, adminPhone }: { sessionId: string; adminPhone?: string }) {
  const pairing = usePairingCode();
  const [phone, setPhone] = useState(adminPhone ?? "");

  // Prefill the admin number once it loads (without clobbering manual edits).
  useEffect(() => {
    if (adminPhone) setPhone((cur) => cur || adminPhone);
  }, [adminPhone]);

  // The code the gateway prints to its console on boot is also pushed as an
  // `auth.code` event; cacheBridge writes it here. Read it so it shows on the
  // dashboard without anyone tailing the logs. A manually requested code lands
  // in the same place (usePairingCode's onSuccess), so this covers both.
  const live = useQuery<{ code?: string } | undefined>({
    queryKey: qk.sessionPairing(sessionId),
    queryFn: () => undefined,
    enabled: false,
    staleTime: Infinity,
  }).data;

  const code = pairing.data?.code ?? live?.code;
  const digits = digitsOnly(phone);

  const submit = (e: React.FormEvent): void => {
    e.preventDefault();
    if (!digits) return;
    pairing.mutate(
      { sessionId, phone: digits },
      {
        onError: (err) => toast.error(isApiError(err) ? err.message : "Failed to request code"),
      },
    );
  };

  return (
    <section>
      <PanelHeading
        icon={<SmartphoneIcon aria-hidden />}
        title="Use a pairing code"
        text="Type an 8-character code on the phone instead of scanning."
      />
      <form onSubmit={submit} className="grid gap-2">
        <Label htmlFor="pair-phone">Phone number with country code</Label>
        <div className="flex gap-2">
          <Input
            id="pair-phone"
            inputMode="tel"
            autoComplete="off"
            placeholder="+62 812 3456 789"
            value={phone}
            onChange={(e) => setPhone(e.target.value)}
          />
          <Button type="submit" disabled={pairing.isPending || !digits}>
            {pairing.isPending ? "Requesting…" : "Get code"}
          </Button>
        </div>
        <p className="text-xs text-muted-foreground">
          Spaces, dashes and “+” are fine. We send only the digits{digits ? ` (${digits})` : ""}.
        </p>
      </form>
      {code ? (
        <div className="mt-4 grid gap-2 rounded-xl border bg-muted/40 p-4 dark:bg-input/20">
          <p className="text-xs text-muted-foreground">Enter this code on the phone</p>
          <div className="flex items-center justify-between gap-2">
            <p className="font-mono text-2xl font-medium tracking-[0.25em]">{code}</p>
            <CopyButton value={code} label="Copy code" iconOnly />
          </div>
          <p className="text-xs text-muted-foreground">
            WhatsApp → Linked devices → Link a device → Link with phone number instead.
          </p>
        </div>
      ) : (
        <p className="mt-4 text-xs text-muted-foreground">
          When the gateway starts, it generates a code for the admin number and it
          appears here automatically. Or request a fresh one above.
        </p>
      )}
    </section>
  );
}
