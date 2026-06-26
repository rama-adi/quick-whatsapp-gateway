// Admin: admin-number pairing — QR display + phone pairing-code entry for the
// platform admin session(s). Ported from v1 admin/pairing.tsx; route shell +
// ./-shared import path changed, behavior identical. Guard from parent /admin.
//
// Data: admin sessions via the gateway GET /admin/sessions (useAdminSessions),
// filtered to isAdminSession. Lifecycle/QR/pairing-code go to the gateway via
// the FROZEN session hooks (browser -> gateway directly, Bearer JWT). The live
// QR refresh rides the shared firehose (auth.qr event refetches the seed query).

import { useEffect, useMemo, useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import {
  SessionStatusBadge,
  StreamIndicator,
  useLiveSessionStatus,
  withLiveStatus,
} from "./-shared";
import { useAdminSessions } from "~/lib/api/hooks/admin";
import {
  useSessionQR,
  useSessionLifecycle,
  usePairingCode,
} from "~/lib/api/hooks/sessions";
import { apiUrl } from "~/lib/api/hooks/_shared";
import type { SessionAction, SessionStatus, WASession } from "~/lib/api/types";
import { isApiError } from "~/lib/api/envelope";
import { Button } from "~/components/ui/button";
import { Input } from "~/components/ui/input";
import { Label } from "~/components/ui/label";
import { Skeleton } from "~/components/ui/skeleton";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "~/components/ui/card";
import { toast } from "sonner";

export const Route = createFileRoute("/_app/admin/pairing")({
  component: AdminPairing,
});

const LIFECYCLE: SessionAction[] = ["start", "stop", "restart", "logout"];

function AdminPairing() {
  const q = useAdminSessions();
  const overrides = useLiveSessionStatus();
  const [selectedId, setSelectedId] = useState<string | null>(null);

  const adminSessions = useMemo<WASession[]>(
    () =>
      (q.data?.pages.flatMap((p) => p.data ?? []) ?? []).filter(
        (s) => s.isAdminSession,
      ),
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
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h1 className="text-xl font-semibold">Admin Pairing</h1>
          <p className="text-sm text-muted-foreground">
            Pair the platform admin WhatsApp number via QR or phone code.
          </p>
        </div>
        <StreamIndicator />
      </div>

      {q.isLoading ? (
        <Skeleton className="h-40 w-full" />
      ) : q.isError ? (
        <Card>
          <CardContent className="space-y-3 py-6 text-center">
            <p className="text-sm text-destructive">
              {isApiError(q.error) ? q.error.message : "Failed to load admin sessions"}
            </p>
            <Button variant="outline" size="sm" onClick={() => void q.refetch()}>
              Retry
            </Button>
          </CardContent>
        </Card>
      ) : adminSessions.length === 0 ? (
        <Card>
          <CardContent className="py-10 text-center text-sm text-muted-foreground">
            No admin session exists. Configure an admin number (ADMIN_*) so a
            pairable admin session is provisioned.
          </CardContent>
        </Card>
      ) : (
        <div className="grid gap-4 lg:grid-cols-[260px_1fr]">
          <SessionPicker
            sessions={adminSessions}
            overrides={overrides}
            selectedId={selectedId}
            onSelect={setSelectedId}
          />
          {selected ? (
            <PairingPanel
              session={selected}
              liveStatus={withLiveStatus(selected, overrides)}
            />
          ) : (
            <Card>
              <CardContent className="py-10 text-center text-sm text-muted-foreground">
                Select an admin session.
              </CardContent>
            </Card>
          )}
        </div>
      )}
    </div>
  );
}

function SessionPicker({
  sessions,
  overrides,
  selectedId,
  onSelect,
}: {
  sessions: WASession[];
  overrides: Record<string, SessionStatus>;
  selectedId: string | null;
  onSelect: (id: string) => void;
}) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Admin sessions</CardTitle>
        <CardDescription>{sessions.length} configured</CardDescription>
      </CardHeader>
      <CardContent className="space-y-1">
        {sessions.map((s) => (
          <button
            key={s.id}
            type="button"
            onClick={() => onSelect(s.id)}
            className={
              "flex w-full items-center justify-between gap-2 rounded-md px-3 py-2 text-left text-sm hover:bg-accent " +
              (selectedId === s.id ? "bg-accent" : "")
            }
          >
            <span className="truncate">
              <span className="font-medium">{s.label || s.id}</span>
              <span className="block font-mono text-xs text-muted-foreground">
                {s.tenantId}
              </span>
            </span>
            <SessionStatusBadge status={withLiveStatus(s, overrides)} />
          </button>
        ))}
      </CardContent>
    </Card>
  );
}

function PairingPanel({
  session,
  liveStatus,
}: {
  session: WASession;
  liveStatus?: SessionStatus;
}) {
  const lifecycle = useSessionLifecycle();
  const needsScan = liveStatus === "scan_qr_code";

  const runAction = (action: SessionAction): void => {
    lifecycle.mutate(
      { sessionId: session.id, action },
      {
        onSuccess: () => toast.success(`Session ${action} requested`),
        onError: (err) => toast.error(isApiError(err) ? err.message : "Action failed"),
      },
    );
  };

  return (
    <Card>
      <CardHeader className="flex-row flex-wrap items-center justify-between gap-2 space-y-0">
        <div>
          <CardTitle className="text-base">{session.label || session.id}</CardTitle>
          <CardDescription className="font-mono text-xs">{session.id}</CardDescription>
        </div>
        <SessionStatusBadge status={liveStatus} />
      </CardHeader>
      <CardContent className="space-y-6">
        <div className="flex flex-wrap gap-2">
          {LIFECYCLE.map((action) => (
            <Button
              key={action}
              size="sm"
              variant="outline"
              disabled={lifecycle.isPending}
              onClick={() => runAction(action)}
            >
              {action}
            </Button>
          ))}
        </div>

        <div className="grid gap-6 md:grid-cols-2">
          <QrPanel sessionId={session.id} needsScan={needsScan} />
          <PairingCodePanel sessionId={session.id} />
        </div>
      </CardContent>
    </Card>
  );
}

function QrPanel({ sessionId, needsScan }: { sessionId: string; needsScan: boolean }) {
  const qr = useSessionQR(sessionId);
  // Bust the image cache when a fresh QR arrives (the auth.qr event refetches
  // the seed query, changing data.code).
  const code = qr.data?.code;
  const imgSrc = useMemo(
    () =>
      `${apiUrl(`/sessions/${encodeURIComponent(sessionId)}/qr`)}?format=image&v=${
        code ? encodeURIComponent(code).slice(0, 16) : "0"
      }`,
    [sessionId, code],
  );

  return (
    <section className="space-y-2">
      <h3 className="text-sm font-medium">Scan QR</h3>
      {!needsScan ? (
        <p className="text-sm text-muted-foreground">
          QR scanning is only available while the session is awaiting a scan.
          Start the session to generate a code.
        </p>
      ) : qr.isLoading ? (
        <Skeleton className="size-48" />
      ) : qr.isError ? (
        <p className="text-sm text-destructive">
          {isApiError(qr.error) ? qr.error.message : "QR unavailable"}
        </p>
      ) : code ? (
        <div className="space-y-2">
          <img
            src={imgSrc}
            alt="WhatsApp pairing QR code"
            width={192}
            height={192}
            className="size-48 rounded-md border bg-white p-2"
          />
          <p className="break-all font-mono text-xs text-muted-foreground">{code}</p>
        </div>
      ) : (
        <p className="text-sm text-muted-foreground">Waiting for a QR code…</p>
      )}
    </section>
  );
}

function PairingCodePanel({ sessionId }: { sessionId: string }) {
  const pairing = usePairingCode();
  const [phone, setPhone] = useState("");

  const submit = (e: React.FormEvent): void => {
    e.preventDefault();
    const trimmed = phone.trim();
    if (!trimmed) return;
    pairing.mutate(
      { sessionId, phone: trimmed },
      {
        onError: (err) =>
          toast.error(isApiError(err) ? err.message : "Failed to request code"),
      },
    );
  };

  return (
    <section className="space-y-2">
      <h3 className="text-sm font-medium">Pair with a phone code</h3>
      <form onSubmit={submit} className="space-y-2">
        <Label htmlFor="pair-phone">Phone number (E.164, no +)</Label>
        <div className="flex gap-2">
          <Input
            id="pair-phone"
            inputMode="numeric"
            placeholder="628123456789"
            value={phone}
            onChange={(e) => setPhone(e.target.value)}
          />
          <Button type="submit" disabled={pairing.isPending || !phone.trim()}>
            {pairing.isPending ? "Requesting…" : "Get code"}
          </Button>
        </div>
      </form>
      {pairing.data?.code && (
        <div className="rounded-md border bg-muted/40 p-3">
          <p className="text-xs text-muted-foreground">Enter this code on the phone:</p>
          <p className="font-mono text-2xl tracking-widest">{pairing.data.code}</p>
        </div>
      )}
    </section>
  );
}
