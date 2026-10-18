// The session overview: a guided page for one WhatsApp session.
//
// - Not linked yet: the "Link your phone" card is the whole page (QR or
//   pairing code), with Start inside the QR panel.
// - Linked: a "next steps" card (celebratory right after linking), a ready-to-run
//   "send a test message" snippet with this session's ID filled in, attachment
//   storage, backup import, and a details column.

import { useEffect, useRef, useState, type ReactNode } from "react";
import { Link, useNavigate } from "@tanstack/react-router";
import {
  ArrowRightIcon,
  ArrowUpRightIcon,
  CheckCircle2Icon,
  InfoIcon,
  KeyRoundIcon,
  MessagesSquareIcon,
  PlayIcon,
  QrCodeIcon,
  RefreshCwIcon,
  SearchXIcon,
  SendIcon,
  SmartphoneIcon,
  TriangleAlertIcon,
  WebhookIcon,
  XIcon,
} from "lucide-react";
import {
  useSession,
  useSessionMe,
  useSessionQR,
  usePairingCode,
} from "~/lib/api/hooks/sessions";
import { isApiError } from "~/lib/api/envelope";
import type { WASession } from "~/lib/api/types";
import { usePollingInterval } from "~/lib/events/useEventStream";
import { useAppSession } from "~/lib/auth/context";
import { usePublicApiUrl } from "~/lib/api/public-url";
import { apiRequestSnippets } from "~/lib/snippets";
import { DOCS } from "~/lib/docs-links";
import { cn } from "~/lib/utils";
import { Button } from "~/components/ui/button";
import { Input } from "~/components/ui/input";
import { Label } from "~/components/ui/label";
import { Skeleton } from "~/components/ui/skeleton";
import { Spinner } from "~/components/ui/spinner";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "~/components/ui/card";
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "~/components/ui/empty";
import { Tabs, TabsList, TabsTrigger } from "~/components/ui/tabs";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "~/components/ui/tooltip";
import { Page, PageTitle } from "~/components/shell/page-title";
import { SessionStatusBadge } from "~/components/status-badge";
import { CodeSnippet } from "~/components/code-snippet";
import { CopyButton, CopyableId } from "~/components/copyable";
import { Time } from "~/components/time";
import { toast } from "sonner";
import {
  QrImage,
  SessionActions,
  formatPhone,
  isRunning,
  normalizePhone,
  sessionName,
  useSessionActions,
} from "./user-ui";
import { SessionStorageCard } from "./session-storage-card";
import { BackupImportCard } from "./backup-import-card";

export function SessionOverview({ sessionId }: { sessionId: string }) {
  const poll = usePollingInterval();
  const actions = useSessionActions();
  const pending = actions.pendingFor(sessionId);
  const session = useSession(sessionId, (query) =>
    query.state.data?.status === "starting" || pending ? 3000 : poll,
  );
  const me = useSessionMe(sessionId);
  const navigate = useNavigate();
  const app = useAppSession();

  // The success moment: this page saw the session go from "not linked" to
  // "linked" (rather than the user opening an already-linked session).
  const paired = Boolean(session.data?.waJid);
  const sawUnpaired = useRef(false);
  const [justConnected, setJustConnected] = useState(false);
  useEffect(() => {
    if (!session.data) return;
    if (!paired) sawUnpaired.current = true;
    else if (sawUnpaired.current) setJustConnected(true);
  }, [paired, session.data]);

  if (session.isLoading) {
    return (
      <Page>
        <div className="space-y-2">
          <Skeleton className="h-7 w-56" />
          <Skeleton className="h-4 w-80" />
        </div>
        <Skeleton className="h-96 w-full rounded-xl" />
      </Page>
    );
  }

  if (session.isError) {
    const notFound = isApiError(session.error) && session.error.code === "not_found";
    return (
      <Empty className="min-h-[50svh]">
        <EmptyHeader>
          <EmptyMedia variant="icon">
            {notFound ? <SearchXIcon aria-hidden /> : <TriangleAlertIcon aria-hidden />}
          </EmptyMedia>
          <EmptyTitle>
            {notFound
              ? app.activeOrg
                ? "This session isn't in your current organization"
                : "No organization selected"
              : "Couldn't load this session"}
          </EmptyTitle>
          <EmptyDescription>
            {notFound
              ? "It may have been deleted, or it belongs to another organization. Switch organizations from the switcher in the header, or go back to your sessions."
              : isApiError(session.error)
                ? session.error.message
                : "Something went wrong while loading the session."}
          </EmptyDescription>
        </EmptyHeader>
        <EmptyContent className="flex-row justify-center">
          {!notFound && (
            <Button variant="outline" onClick={() => void session.refetch()}>
              <RefreshCwIcon aria-hidden data-icon="inline-start" />
              Retry
            </Button>
          )}
          <Button asChild variant={notFound ? "default" : "ghost"}>
            <Link to="/user/sessions">Back to sessions</Link>
          </Button>
        </EmptyContent>
      </Empty>
    );
  }

  const s = session.data;
  if (!s) return null;

  const phone = formatPhone(s.phoneNumber);

  return (
    <Page>
      <PageTitle
        title={
          <>
            <span className="truncate">{sessionName(s)}</span>
            <SessionStatusBadge status={s.status} className="shrink-0" />
          </>
        }
        description={
          <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
            {phone ? (
              <span className="font-medium text-foreground/80 tabular-nums">{phone}</span>
            ) : (
              <span>Not linked to a phone yet</span>
            )}
            <span aria-hidden className="hidden text-border sm:inline">
              ·
            </span>
            <span className="inline-flex items-center gap-1">
              Session ID
              <CopyableId value={s.id} label="Copy session ID" />
            </span>
          </span>
        }
        actions={
          <SessionActions
            session={s}
            actions={actions}
            size="default"
            showPrimary={paired}
            onDeleted={() => void navigate({ to: "/user/sessions" })}
          />
        }
      />

      {!paired ? (
        <PairingCard session={s} actions={actions} />
      ) : (
        <>
          <StatusBanner session={s} actions={actions} />
          <NextSteps session={s} justConnected={justConnected} />
          <div className="grid items-start gap-6 xl:grid-cols-[minmax(0,1fr)_20rem]">
            <div className="grid min-w-0 gap-6">
              <SendTestCard session={s} />
              <SessionStorageCard sessionId={s.id} />
              <BackupImportCard sessionId={s.id} />
            </div>
            <DetailsCard session={s} lid={s.waLid || me.data?.waLid} />
          </div>
        </>
      )}
    </Page>
  );
}

// --- Linked: status banner -------------------------------------------------------

/** A slim notice when a linked session isn't connected right now. */
function StatusBanner({
  session,
  actions,
}: {
  session: WASession;
  actions: ReturnType<typeof useSessionActions>;
}) {
  const pending = actions.pendingFor(session.id);
  const copy: Record<string, { text: string; action?: "start" | "restart" }> = {
    stopped: {
      text: "This session is stopped, so it isn't receiving new messages. Start it to reconnect.",
      action: "start",
    },
    failed: {
      text: "The connection failed and the gateway stopped retrying. Restart the session to try again.",
      action: "restart",
    },
    starting: { text: "Reconnecting to WhatsApp…" },
    scan_qr_code: { text: "Waiting for the phone to confirm the link…" },
    logged_out: {
      text: "The phone unlinked this session. Link it again to reconnect.",
      action: "start",
    },
  };
  const entry = copy[session.status];
  if (!entry) return null;
  const tone =
    session.status === "failed"
      ? "border-destructive/30 bg-destructive/5"
      : "border-border bg-muted/40";

  return (
    <div
      role="status"
      className={cn(
        "flex flex-col gap-3 rounded-xl border px-4 py-3 text-sm sm:flex-row sm:items-center sm:justify-between",
        tone,
      )}
    >
      <p className="flex items-center gap-2">
        {entry.action ? (
          <InfoIcon className="size-4 shrink-0 text-muted-foreground" aria-hidden />
        ) : (
          <Spinner className="text-muted-foreground" />
        )}
        {entry.text}
      </p>
      {entry.action && (
        <Button
          size="sm"
          className="shrink-0 self-start sm:self-auto"
          disabled={Boolean(pending)}
          onClick={() => void actions.run(session, entry.action!)}
        >
          {entry.action === "start" ? (
            <PlayIcon aria-hidden data-icon="inline-start" />
          ) : (
            <RefreshCwIcon aria-hidden data-icon="inline-start" />
          )}
          {entry.action === "start" ? "Start session" : "Restart session"}
        </Button>
      )}
    </div>
  );
}

// --- Linked: next steps -----------------------------------------------------------

const dismissKey = (id: string) => `qwg:next-steps-dismissed:${id}`;

function NextSteps({
  session,
  justConnected,
}: {
  session: WASession;
  justConnected: boolean;
}) {
  // Read after mount (localStorage isn't available during SSR).
  const [dismissed, setDismissed] = useState(true);
  useEffect(() => {
    try {
      setDismissed(window.localStorage.getItem(dismissKey(session.id)) === "1");
    } catch {
      setDismissed(false);
    }
  }, [session.id]);

  if (!justConnected && (dismissed || session.status !== "working")) return null;

  const dismiss = (): void => {
    setDismissed(true);
    try {
      window.localStorage.setItem(dismissKey(session.id), "1");
    } catch {
      // Private mode: hide for this visit only.
    }
  };

  const phone = formatPhone(session.phoneNumber);
  const steps: Array<{
    icon: typeof KeyRoundIcon;
    title: string;
    body: string;
    link: ReactNode;
  }> = [
    {
      icon: KeyRoundIcon,
      title: "Create an API key",
      body: "Lets your server call the gateway.",
      link: <Link to="/user/keys" className="absolute inset-0" aria-label="Create an API key" />,
    },
    {
      icon: SendIcon,
      title: "Send a test message",
      body: "Run the snippet below from a terminal.",
      link: <a href="#send-test" className="absolute inset-0" aria-label="Send a test message" />,
    },
    {
      icon: WebhookIcon,
      title: "Add a webhook",
      body: "Get new messages pushed to your server.",
      link: <Link to="/user/webhooks" className="absolute inset-0" aria-label="Add a webhook" />,
    },
    {
      icon: MessagesSquareIcon,
      title: "Open chats",
      body: "Read and reply from the dashboard.",
      link: (
        <Link
          to="/user/sessions/$sessionId/chats"
          params={{ sessionId: session.id }}
          className="absolute inset-0"
          aria-label="Open chats"
        />
      ),
    },
  ];

  return (
    <section
      aria-label="Next steps"
      className={cn(
        "@container relative rounded-xl border p-5",
        justConnected
          ? "border-emerald-500/30 bg-emerald-500/[0.06] dark:bg-emerald-500/10"
          : "bg-card",
      )}
    >
      <Button
        variant="ghost"
        size="icon-sm"
        className="absolute top-3 right-3 text-muted-foreground"
        onClick={dismiss}
        aria-label="Hide next steps"
      >
        <XIcon aria-hidden />
      </Button>
      <div className="flex items-start gap-3 pr-8">
        {justConnected && (
          <CheckCircle2Icon
            className="mt-0.5 size-5 shrink-0 text-emerald-600 dark:text-emerald-400"
            aria-hidden
          />
        )}
        <div className="space-y-1">
          <h2 className="font-semibold tracking-tight">
            {justConnected
              ? `You're connected${phone ? ` as ${phone}` : ""}`
              : "Next steps"}
          </h2>
          <p className="text-sm text-muted-foreground">
            {justConnected
              ? "Your number is live. Here's what most people do next."
              : "Your number is connected. Pick up where you left off."}
          </p>
        </div>
      </div>
      <ul className="mt-4 grid gap-2 @lg:grid-cols-2 @5xl:grid-cols-4">
        {steps.map(({ icon: Icon, title, body, link }) => (
          <li
            key={title}
            className="group relative flex items-start gap-3 rounded-lg border bg-background/70 p-3 transition-colors duration-150 ease-soft hover:bg-muted/60 focus-within:ring-3 focus-within:ring-ring/30"
          >
            <span className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-muted text-foreground ring-1 ring-border">
              <Icon className="size-4" aria-hidden />
            </span>
            <span className="min-w-0 flex-1">
              <span className="flex items-center gap-1 text-sm font-medium">
                {title}
                <ArrowRightIcon
                  className="size-3.5 text-muted-foreground transition-transform duration-150 ease-soft group-hover:translate-x-0.5"
                  aria-hidden
                />
              </span>
              <span className="block text-xs text-muted-foreground">{body}</span>
            </span>
            {link}
          </li>
        ))}
      </ul>
    </section>
  );
}

// --- Linked: send a test message ----------------------------------------------------

function SendTestCard({ session }: { session: WASession }) {
  const apiBase = usePublicApiUrl();
  const [to, setTo] = useState(formatPhone(session.phoneNumber) ?? "");
  const digits = normalizePhone(to);
  const self = digits !== "" && digits === session.phoneNumber;
  const recipient = digits ? `${digits}@s.whatsapp.net` : "628123456789@s.whatsapp.net";

  const tabs = apiRequestSnippets({
    method: "POST",
    url: `${apiBase}/sessions/${session.id}/messages`,
    token: "$WA_API_KEY",
    body: { type: "text", to: recipient, text: "Hello from WA Gateway" },
  });

  return (
    <Card id="send-test" className="scroll-mt-6">
      <CardHeader>
        <CardTitle>Send a test message</CardTitle>
        <CardDescription>
          Call the API from your own code. This session's ID is already filled
          in; replace <code className="font-mono text-xs">$WA_API_KEY</code> with
          a key from{" "}
          <Link to="/user/keys" className="font-medium text-foreground/80 underline-offset-4 hover:text-foreground hover:underline">
            API keys
          </Link>
          .
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-4">
        <div className="grid gap-4 sm:grid-cols-2">
          <div className="grid gap-1.5">
            <Label htmlFor="test-to">Send to</Label>
            <Input
              id="test-to"
              inputMode="tel"
              value={to}
              onChange={(e) => setTo(e.target.value)}
              placeholder="+62 812 3456 789"
              autoComplete="off"
            />
            <p className="text-xs text-muted-foreground">
              {self
                ? "Your own number: the message lands in “Message yourself”."
                : "Phone number with country code."}
            </p>
          </div>
          <div className="grid content-start gap-1.5">
            <span className="text-sm font-medium">API endpoint</span>
            <div className="flex h-8 items-center rounded-lg border bg-muted/40 pr-1 pl-2.5">
              <CopyableId value={apiBase} label="Copy API endpoint" className="w-full justify-between" />
            </div>
            <p className="text-xs text-muted-foreground">Base URL for every API call.</p>
          </div>
        </div>
        <CodeSnippet tabs={tabs} />
        <div className="flex flex-wrap gap-x-4 gap-y-1 text-sm">
          <a
            href={DOCS.sending}
            target="_blank"
            rel="noreferrer"
            className="inline-flex items-center gap-0.5 font-medium text-foreground/80 underline-offset-4 hover:text-foreground hover:underline"
          >
            Other message types
            <ArrowUpRightIcon className="size-3.5" aria-hidden />
          </a>
          <a
            href={DOCS.receiving}
            target="_blank"
            rel="noreferrer"
            className="inline-flex items-center gap-0.5 font-medium text-foreground/80 underline-offset-4 hover:text-foreground hover:underline"
          >
            Receive messages
            <ArrowUpRightIcon className="size-3.5" aria-hidden />
          </a>
        </div>
      </CardContent>
    </Card>
  );
}

// --- Linked: details --------------------------------------------------------------

function DetailsCard({ session, lid }: { session: WASession; lid: string | undefined }) {
  const phone = formatPhone(session.phoneNumber);
  return (
    <Card>
      <CardHeader>
        <CardTitle>Details</CardTitle>
      </CardHeader>
      <CardContent>
        <dl className="grid gap-x-6 gap-y-3.5 text-sm sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-1">
          <Detail label="Phone">
            {phone ? (
              <CopyableId value={phone} label="Copy phone number" className="[&>span:first-child]:font-sans [&>span:first-child]:text-sm" />
            ) : (
              "—"
            )}
          </Detail>
          <Detail
            label="WhatsApp ID (JID)"
            info="The WhatsApp address of this number. Use JIDs like this as `to` when sending."
          >
            {session.waJid ? (
              <CopyableId value={session.waJid} truncate={[14, 10]} label="Copy JID" />
            ) : (
              "—"
            )}
          </Detail>
          <Detail
            label="LID"
            info="WhatsApp's privacy-preserving ID for this account. Some events identify people by LID instead of phone number."
          >
            {lid ? <CopyableId value={lid} truncate={[14, 6]} label="Copy LID" /> : "—"}
          </Detail>
          <Detail
            label="Send rate limit"
            info="Messages sent above this rate are queued and go out as the limit allows."
          >
            <span className="tabular-nums">{formatRateLimit(session)}</span>
          </Detail>
          <Detail label="Auto-read" info="Inbound messages are marked as read automatically. Set when the session was created.">
            {session.autoRead ? "On" : "Off"}
          </Detail>
          <Detail label="Typing indicator" info="Shows “typing…” to the recipient while a message is sent. Set when the session was created.">
            {session.presenceTyping ? "On" : "Off"}
          </Detail>
          <Detail label="Last connected">
            <Time value={session.lastConnectedAt} fallback="Never" />
          </Detail>
          <Detail label="Created">
            <Time value={session.createdAt} />
          </Detail>
        </dl>
      </CardContent>
    </Card>
  );
}

/** "20/min · 300/hour"; a 0 means that window has no limit. */
function formatRateLimit(s: WASession): string {
  if (!s.ratePerMin && !s.ratePerHour) return "No limit";
  const perMin = s.ratePerMin ? `${s.ratePerMin}/min` : "no per-minute limit";
  const perHour = s.ratePerHour ? `${s.ratePerHour}/hour` : "no hourly limit";
  return `${perMin} · ${perHour}`;
}

function Detail({
  label,
  info,
  children,
}: {
  label: string;
  info?: string;
  children: ReactNode;
}) {
  return (
    <div className="grid min-w-0 gap-0.5">
      <dt className="flex items-center gap-1 text-xs font-medium text-muted-foreground">
        {label}
        {info && <InfoTip text={info} label={`About ${label}`} />}
      </dt>
      <dd className="min-w-0">{children}</dd>
    </div>
  );
}

function InfoTip({ text, label }: { text: string; label: string }) {
  return (
    <TooltipProvider delayDuration={200}>
      <Tooltip>
        <TooltipTrigger asChild>
          <button
            type="button"
            aria-label={label}
            className="inline-flex rounded-full text-muted-foreground/70 outline-none hover:text-foreground focus-visible:ring-3 focus-visible:ring-ring/30"
          >
            <InfoIcon className="size-3.5" aria-hidden />
          </button>
        </TooltipTrigger>
        <TooltipContent className="max-w-64">{text}</TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
}

// --- Not linked: pairing ----------------------------------------------------------

type PairMode = "qr" | "code";

const STEPS: Record<PairMode, ReactNode[]> = {
  qr: [
    "Open WhatsApp on the phone you want to connect.",
    <>
      Go to <b className="font-medium">Settings → Linked devices</b> and tap{" "}
      <b className="font-medium">Link a device</b>.
    </>,
    "Point the phone's camera at the QR code.",
  ],
  code: [
    "Enter the phone's number here and get a pairing code.",
    <>
      In WhatsApp, go to <b className="font-medium">Settings → Linked devices → Link a device</b>{" "}
      and tap <b className="font-medium">Link with phone number instead</b>.
    </>,
    "Type the 8-character code shown here.",
  ],
};

function PairingCard({
  session,
  actions,
}: {
  session: WASession;
  actions: ReturnType<typeof useSessionActions>;
}) {
  const [mode, setMode] = useState<PairMode>("qr");

  return (
    <Card>
      <CardHeader className="gap-3 sm:grid-cols-[1fr_auto] sm:items-center">
        <div className="grid gap-1">
          <CardTitle className="text-lg">Link your phone</CardTitle>
          <CardDescription>
            Connect a WhatsApp number to this session, the same way you'd link
            WhatsApp Web.{" "}
            <a
              href={DOCS.connectNumber}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-0.5 font-medium whitespace-nowrap text-foreground/80 underline-offset-4 hover:text-foreground hover:underline"
            >
              Learn more
              <ArrowUpRightIcon className="size-3.5" aria-hidden />
            </a>
          </CardDescription>
        </div>
        <Tabs value={mode} onValueChange={(v) => setMode(v as PairMode)}>
          <TabsList>
            <TabsTrigger value="qr" className="gap-1.5">
              <QrCodeIcon className="size-4" aria-hidden />
              QR code
            </TabsTrigger>
            <TabsTrigger value="code" className="gap-1.5">
              <SmartphoneIcon className="size-4" aria-hidden />
              Phone number
            </TabsTrigger>
          </TabsList>
        </Tabs>
      </CardHeader>
      <CardContent>
        <div className="grid items-center gap-8 rounded-xl border bg-muted/30 p-5 md:grid-cols-[minmax(0,28rem)_18rem] md:justify-center md:gap-x-16 md:p-8">
          <ol className="grid gap-4 md:max-w-md">
            {STEPS[mode].map((step, i) => (
              <li key={i} className="flex gap-3 text-sm">
                <span className="flex size-6 shrink-0 items-center justify-center rounded-full bg-background text-xs font-semibold tabular-nums ring-1 ring-border">
                  {i + 1}
                </span>
                <span className="pt-0.5 text-pretty">{step}</span>
              </li>
            ))}
            {(isRunning(session.status) || mode === "code") && (
              <li className="flex items-center gap-2 pt-1 text-xs text-muted-foreground">
                <Spinner className="size-3.5" />
                This page updates by itself once the phone is linked.
              </li>
            )}
          </ol>
          <div className="flex justify-center">
            {mode === "qr" ? (
              <QrPanel session={session} actions={actions} />
            ) : (
              <PairingCodePanel sessionId={session.id} />
            )}
          </div>
        </div>
      </CardContent>
    </Card>
  );
}

/** A fixed-size square so the QR panel doesn't jump between states. */
function QrFrame({ children }: { children: ReactNode }) {
  return (
    <div className="flex size-56 flex-col items-center justify-center gap-3 rounded-xl border border-dashed bg-background p-5 text-center">
      {children}
    </div>
  );
}

/** How long "Connecting…" may show before offering a restart. */
const SLOW_START_MS = 20_000;

/** Spinner for a starting session; after SLOW_START_MS it offers a restart. */
function ConnectingFrame({
  canRestart,
  onRestart,
}: {
  canRestart: boolean;
  onRestart: () => void;
}) {
  const [slow, setSlow] = useState(false);
  useEffect(() => {
    const t = window.setTimeout(() => setSlow(true), SLOW_START_MS);
    return () => window.clearTimeout(t);
  }, []);

  return (
    <QrFrame>
      <Spinner className="size-5 text-muted-foreground" />
      <p className="text-sm text-muted-foreground">
        {slow
          ? "This is taking longer than usual. Restarting the session usually brings the QR code up."
          : "Connecting… the QR code appears in a few seconds."}
      </p>
      {slow && canRestart ? (
        <Button variant="outline" size="sm" onClick={onRestart}>
          <RefreshCwIcon aria-hidden data-icon="inline-start" />
          Restart session
        </Button>
      ) : null}
    </QrFrame>
  );
}

function QrPanel({
  session,
  actions,
}: {
  session: WASession;
  actions: ReturnType<typeof useSessionActions>;
}) {
  const ready = session.status === "scan_qr_code";
  // GET /qr starts QR pairing when none exists, so only issue it once the
  // session is waiting for a scan. That lets the user choose phone-code pairing
  // without an accidental QR client competing with PairPhone.
  const qr = useSessionQR(session.id, ready);
  const poll = usePollingInterval();
  const pending = actions.pendingFor(session.id);

  useEffect(() => {
    if (!poll || !ready) return;
    const t = window.setInterval(() => void qr.refetch(), poll);
    return () => window.clearInterval(t);
  }, [poll, qr, ready]);

  if (session.status === "starting" || pending === "start" || pending === "restart") {
    return (
      <ConnectingFrame
        // Remount on each new attempt so the slow-start timer starts over.
        key={pending ?? "starting"}
        canRestart={!pending}
        onRestart={() => void actions.run(session, "restart")}
      />
    );
  }

  if (!isRunning(session.status)) {
    return (
      <QrFrame>
        <QrCodeIcon className="size-8 text-muted-foreground/60" aria-hidden />
        <p className="text-sm text-muted-foreground">
          Start the session to show a QR code.
        </p>
        <Button onClick={() => void actions.run(session, "start")}>
          <PlayIcon aria-hidden data-icon="inline-start" />
          Start session
        </Button>
      </QrFrame>
    );
  }

  if (!ready || qr.isLoading) {
    return <Skeleton className="size-56 rounded-xl" />;
  }

  if (qr.isError || !qr.data?.code) {
    return (
      <QrFrame>
        <p className="text-sm text-muted-foreground">
          {qr.isError && isApiError(qr.error)
            ? qr.error.message
            : "Waiting for a QR code…"}
        </p>
        <Button variant="outline" size="sm" onClick={() => void qr.refetch()}>
          <RefreshCwIcon aria-hidden data-icon="inline-start" />
          Refresh
        </Button>
      </QrFrame>
    );
  }

  return (
    <div className="flex flex-col items-center gap-2">
      <QrImage code={qr.data.code} hint={false} />
      <p className="text-xs text-muted-foreground">Refreshes automatically</p>
    </div>
  );
}

/** "ABCD1234" → "ABCD-1234", the way WhatsApp shows it. */
function formatPairingCode(code: string): string {
  const raw = code.replace(/[^0-9A-Za-z]/g, "");
  return raw.length === 8 ? `${raw.slice(0, 4)}-${raw.slice(4)}` : code;
}

function PairingCodePanel({ sessionId }: { sessionId: string }) {
  const [phone, setPhone] = useState("");
  const [touched, setTouched] = useState(false);
  const pairing = usePairingCode();
  const digits = normalizePhone(phone);
  const tooShort = digits.length > 0 && digits.length < 8;
  const invalid = touched && (digits.length === 0 || tooShort);

  const submit = (e: React.FormEvent): void => {
    e.preventDefault();
    setTouched(true);
    if (digits.length < 8) return;
    pairing.mutate(
      { sessionId, phone: digits },
      {
        onError: (err) =>
          toast.error(isApiError(err) ? err.message : "Couldn't get a pairing code"),
      },
    );
  };

  const code = pairing.data?.code;

  return (
    <div className="grid w-full max-w-72 gap-4">
      <form onSubmit={submit} className="grid gap-3" noValidate>
        <div className="grid gap-1.5">
          <Label htmlFor="pairing-phone">Phone number</Label>
          <Input
            id="pairing-phone"
            inputMode="tel"
            autoComplete="tel"
            value={phone}
            aria-invalid={invalid || undefined}
            aria-describedby="pairing-phone-hint"
            onChange={(e) => setPhone(e.target.value)}
            onBlur={() => phone && setTouched(true)}
            placeholder="+62 812 3456 789"
            className="bg-background"
          />
          <p
            id="pairing-phone-hint"
            className={cn("text-xs", invalid ? "text-destructive" : "text-muted-foreground")}
          >
            {invalid
              ? digits.length === 0
                ? "Enter the number with its country code."
                : "That looks too short. Include the country code."
              : digits
                ? `Will request a code for +${digits}`
                : "With country code, e.g. +62 for Indonesia."}
          </p>
        </div>
        <Button type="submit" disabled={pairing.isPending}>
          {pairing.isPending ? "Requesting…" : code ? "Get a new code" : "Get pairing code"}
        </Button>
      </form>

      {code && (
        <div className="grid gap-2 rounded-xl border bg-background p-4 text-center">
          <p className="text-xs text-muted-foreground">Enter this code on your phone</p>
          <code className="font-mono text-2xl font-semibold tracking-[0.2em]">
            {formatPairingCode(code)}
          </code>
          <CopyButton value={code} label="Copy code" className="justify-self-center" />
          <p className="text-xs text-pretty text-muted-foreground">
            The code expires after a few minutes. If it stops working, get a new one.
          </p>
        </div>
      )}
    </div>
  );
}
