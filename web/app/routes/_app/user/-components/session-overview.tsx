import { useEffect, useState } from "react";
import { LinkIcon, RefreshCwIcon, SmartphoneIcon } from "lucide-react";
import {
  useSession,
  useSessionLifecycle,
  useSessionMe,
  useSessionQR,
  usePairingCode,
} from "~/lib/api/hooks/sessions";
import { isApiError } from "~/lib/api/envelope";
import type { SessionAction, WASession } from "~/lib/api/types";
import { usePollingInterval } from "~/lib/events/useEventStream";
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
import { Tabs, TabsContent, TabsList, TabsTrigger } from "~/components/ui/tabs";
import { toast } from "sonner";
import {
  CopyButton,
  formatTimestamp,
  QrImage,
  SessionStatusBadge,
} from "./user-ui";

const LIFECYCLE: SessionAction[] = ["start", "stop", "restart", "logout"];

export function SessionOverview({ sessionId }: { sessionId: string }) {
  const poll = usePollingInterval();
  const session = useSession(sessionId);
  const me = useSessionMe(sessionId);
  const lifecycle = useSessionLifecycle();

  // When the stream is degraded, poll the session row so the status badge keeps
  // advancing through starting -> scan_qr_code -> working.
  useEffect(() => {
    if (!poll || !sessionId) return;
    const t = window.setInterval(() => void session.refetch(), poll);
    return () => window.clearInterval(t);
  }, [poll, sessionId, session]);

  if (session.isLoading) {
    return (
      <div className="grid gap-4 md:grid-cols-2">
        <Skeleton className="h-48 w-full" />
        <Skeleton className="h-48 w-full" />
      </div>
    );
  }

  if (session.isError) {
    return (
      <Card>
        <CardContent className="flex flex-col items-center gap-3 py-10 text-center">
          <p className="text-sm text-destructive">
            {isApiError(session.error)
              ? session.error.message
              : "Failed to load session"}
          </p>
          <Button
            variant="outline"
            size="sm"
            className="gap-1.5"
            onClick={() => void session.refetch()}
          >
            <RefreshCwIcon className="size-4" aria-hidden />
            Retry
          </Button>
        </CardContent>
      </Card>
    );
  }

  const s = session.data;
  if (!s) return null;

  const run = (action: SessionAction): void => {
    lifecycle.mutate(
      { sessionId, action },
      {
        onError: (err) =>
          toast.error(isApiError(err) ? err.message : "Action failed"),
        onSuccess: () => toast.success(`Session ${action} requested`),
      },
    );
  };

  const needsPairing =
    s.status === "scan_qr_code" ||
    s.status === "starting" ||
    s.status === "stopped";

  return (
    <div className="space-y-4">
      <SessionHeader session={s} pending={lifecycle.isPending} onAction={run} />

      <div className="grid gap-4 md:grid-cols-2">
        <IdentityCard session={s} me={me} />
        {needsPairing ? (
          <PairingCard sessionId={sessionId} status={s.status} />
        ) : (
          <ConnectedCard session={s} />
        )}
      </div>
    </div>
  );
}

function SessionHeader({
  session,
  pending,
  onAction,
}: {
  session: WASession;
  pending: boolean;
  onAction: (action: SessionAction) => void;
}) {
  return (
    <Card>
      <CardHeader className="flex-row items-start justify-between gap-2 space-y-0">
        <div className="space-y-1">
          <CardTitle>{session.label || session.id}</CardTitle>
          <CardDescription className="font-mono text-xs">
            {session.id}
          </CardDescription>
        </div>
        <SessionStatusBadge status={session.status} />
      </CardHeader>
      <CardContent className="flex flex-wrap gap-2">
        {LIFECYCLE.map((action) => (
          <Button
            key={action}
            size="sm"
            variant="outline"
            disabled={pending}
            onClick={() => onAction(action)}
          >
            {action}
          </Button>
        ))}
      </CardContent>
    </Card>
  );
}

function IdentityCard({
  session,
  me,
}: {
  session: WASession;
  me: ReturnType<typeof useSessionMe>;
}) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Identity</CardTitle>
        <CardDescription>The connected WhatsApp account.</CardDescription>
      </CardHeader>
      <CardContent className="space-y-2 text-sm">
        <Row label="Phone">
          {session.phoneNumber ? `+${session.phoneNumber}` : "-"}
        </Row>
        <Row label="JID">
          <span className="font-mono text-xs break-all">
            {session.waJid || me.data?.jid || "-"}
          </span>
        </Row>
        <Row label="Push name">
          {me.isLoading
            ? "..."
            : isApiError(me.error) && me.error.isNotImplemented
              ? "not available"
              : me.data?.pushName || "-"}
        </Row>
        <Row label="Auto-read">{session.autoRead ? "on" : "off"}</Row>
        <Row label="Presence typing">
          {session.presenceTyping ? "on" : "off"}
        </Row>
        <Row label="Rate limit">
          {session.ratePerMin}/min / {session.ratePerHour}/hr
        </Row>
        <Row label="Last connected">
          {formatTimestamp(session.lastConnectedAt)}
        </Row>
      </CardContent>
    </Card>
  );
}

function ConnectedCard({ session }: { session: WASession }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Connection</CardTitle>
        <CardDescription>
          This session is {(session.status ?? "").replace(/_/g, " ")}.
        </CardDescription>
      </CardHeader>
      <CardContent className="text-sm text-muted-foreground">
        {session.status === "working"
          ? "The device is paired and online. Use Logout to unpair, or Restart to reconnect."
          : "No pairing needed in the current state. Start the session to pair a device."}
      </CardContent>
    </Card>
  );
}

function PairingCard({
  sessionId,
  status,
}: {
  sessionId: string;
  status: WASession["status"];
}) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Pair a device</CardTitle>
        <CardDescription>
          Scan the QR with WhatsApp, or request a code to type on your phone.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <Tabs defaultValue="qr">
          <TabsList className="grid w-full grid-cols-2">
            <TabsTrigger value="qr" className="gap-1.5">
              <LinkIcon className="size-4" aria-hidden />
              QR code
            </TabsTrigger>
            <TabsTrigger value="code" className="gap-1.5">
              <SmartphoneIcon className="size-4" aria-hidden />
              Pairing code
            </TabsTrigger>
          </TabsList>
          <TabsContent value="qr" className="pt-4">
            <QrPanel sessionId={sessionId} ready={status === "scan_qr_code"} />
          </TabsContent>
          <TabsContent value="code" className="pt-4">
            <PairingCodePanel sessionId={sessionId} />
          </TabsContent>
        </Tabs>
      </CardContent>
    </Card>
  );
}

function QrPanel({ sessionId, ready }: { sessionId: string; ready: boolean }) {
  const qr = useSessionQR(sessionId);
  const poll = usePollingInterval();

  useEffect(() => {
    if (!poll) return;
    const t = window.setInterval(() => void qr.refetch(), poll);
    return () => window.clearInterval(t);
  }, [poll, qr]);

  if (qr.isLoading) {
    return <Skeleton className="mx-auto size-56" />;
  }

  if (qr.isError) {
    return (
      <div className="flex flex-col items-center gap-3 py-6 text-center">
        <p className="text-sm text-muted-foreground">
          {isApiError(qr.error)
            ? qr.error.message
            : "No QR available yet. Start the session to generate one."}
        </p>
        <Button
          variant="outline"
          size="sm"
          className="gap-1.5"
          onClick={() => void qr.refetch()}
        >
          <RefreshCwIcon className="size-4" aria-hidden />
          Refresh
        </Button>
      </div>
    );
  }

  if (!qr.data?.code) {
    return (
      <p className="py-6 text-center text-sm text-muted-foreground">
        {ready
          ? "Waiting for a QR code..."
          : "Start the session to generate a QR code."}
      </p>
    );
  }

  return <QrImage sessionId={sessionId} code={qr.data.code} />;
}

function PairingCodePanel({ sessionId }: { sessionId: string }) {
  const [phone, setPhone] = useState("");
  const pairing = usePairingCode();

  const submit = (e: React.FormEvent): void => {
    e.preventDefault();
    const cleaned = phone.replace(/[^\d]/g, "");
    if (!cleaned) {
      toast.error("Enter the phone number in international format.");
      return;
    }
    pairing.mutate(
      { sessionId, phone: cleaned },
      {
        onError: (err) =>
          toast.error(isApiError(err) ? err.message : "Failed to request code"),
        onSuccess: () => toast.success("Pairing code generated"),
      },
    );
  };

  return (
    <form onSubmit={submit} className="space-y-4">
      <div className="space-y-2">
        <Label htmlFor="pairing-phone">Phone number</Label>
        <Input
          id="pairing-phone"
          inputMode="numeric"
          value={phone}
          onChange={(e) => setPhone(e.target.value)}
          placeholder="628123456789"
        />
        <p className="text-xs text-muted-foreground">
          International format, digits only (country code + number).
        </p>
      </div>
      <Button type="submit" disabled={pairing.isPending}>
        {pairing.isPending ? "Requesting..." : "Request pairing code"}
      </Button>

      {pairing.data?.code && (
        <div className="space-y-2 rounded-md border bg-muted/40 p-3">
          <p className="text-xs text-muted-foreground">
            Enter this code on your phone (WhatsApp {"->"} Linked devices{" "}
            {"->"} Link with phone number):
          </p>
          <div className="flex items-center justify-between gap-2">
            <code className="text-lg font-semibold tracking-widest">
              {pairing.data.code}
            </code>
            <CopyButton value={pairing.data.code} label="Copy code" />
          </div>
        </div>
      )}
    </form>
  );
}

function Row({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}) {
  return (
    <div className="flex items-start justify-between gap-4">
      <span className="text-muted-foreground">{label}</span>
      <span className="text-right">{children}</span>
    </div>
  );
}
