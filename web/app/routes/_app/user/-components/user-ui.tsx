// Surface-local UI helpers for the user panel's session pages (list, overview).
// Colocated under -components/ (the leading "-" excludes it from file-based
// route generation). Session lifecycle actions live here so the list and the
// overview share one set of rules: which actions apply in which state, the
// confirmations for the destructive ones, and human toast copy.

import { useState } from "react";
import {
  LogOutIcon,
  MoreHorizontalIcon,
  PlayIcon,
  RotateCwIcon,
  SquareIcon,
  Trash2Icon,
} from "lucide-react";
import { toast } from "sonner";
import { Button } from "~/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "~/components/ui/dropdown-menu";
import { useConfirm } from "~/components/confirm-dialog";
import { useDeleteSession, useSessionLifecycle } from "~/lib/api/hooks/sessions";
import { isApiError } from "~/lib/api/envelope";
import type { SessionAction, SessionStatus, WASession } from "~/lib/api/types";
import { QrCode } from "~/routes/-oauth/QrCode";
import { formatDateTime, formatPhone, sessionTitle } from "~/lib/format";

/** @deprecated Use `<Time>` (components/time) or `formatDateTime` (lib/format); local time. */
export function formatTimestamp(ms: number | undefined): string {
  return formatDateTime(ms);
}

/** Map a session status to a Badge variant for consistent coloring. */
export function statusVariant(
  status: SessionStatus | undefined,
): "default" | "secondary" | "destructive" | "outline" {
  switch (status) {
    case "working":
      return "default";
    case "scan_qr_code":
    case "starting":
      return "secondary";
    case "failed":
    case "logged_out":
      return "destructive";
    case "stopped":
    default:
      return "outline";
  }
}

/** @deprecated Import `CopyButton` from `~/components/copyable` instead. */
export { CopyButton } from "~/components/copyable";

/** @deprecated Import `SessionStatusBadge` from `~/components/status-badge` instead. */
export { SessionStatusBadge } from "~/components/status-badge";

// --- Small formatters ---------------------------------------------------------

/** The name to show for a session: its label, else its number, else "Untitled session". */
export const sessionName = sessionTitle;

export { formatPhone };

/** Keep only the digits of a phone typed as "+62 812-3456-789". */
export function normalizePhone(input: string): string {
  return input.replace(/[^\d]/g, "");
}

/** Whether the session is (or is about to be) connected, i.e. "Stop" applies. */
export function isRunning(status: SessionStatus | undefined): boolean {
  return status === "working" || status === "starting" || status === "scan_qr_code";
}

/** One-line hint for a non-connected state, shown under the session name. */
export function statusHint(s: WASession): string | undefined {
  const paired = Boolean(s.waJid);
  switch (s.status) {
    case "scan_qr_code":
      return "Waiting for you to scan the QR code.";
    case "starting":
      return paired ? "Reconnecting to WhatsApp…" : "Connecting to WhatsApp…";
    case "stopped":
      return paired ? "Stopped. Start it to receive new messages." : undefined;
    case "logged_out":
      return "The phone unlinked this session. Link it again to reconnect.";
    case "failed":
      return "The connection failed. Try restarting the session.";
    default:
      return undefined;
  }
}

// --- Lifecycle actions ----------------------------------------------------------

const ACTION_TOAST: Record<SessionAction, (name: string) => string> = {
  start: (n) => `Starting ${n}…`,
  stop: (n) => `Stopped ${n}`,
  restart: (n) => `Restarting ${n}…`,
  logout: (n) => `Logged out ${n}`,
};

const errMessage = (err: unknown, fallback: string): string =>
  isApiError(err) ? err.message : err instanceof Error && err.message ? err.message : fallback;

/**
 * Session lifecycle + delete with confirmations. `run` confirms "Log out";
 * `remove` offers "Log out and delete" when a number is still linked (the API
 * docs ask for `:logout` before delete) and chains both calls.
 */
export function useSessionActions() {
  const lifecycle = useSessionLifecycle();
  const del = useDeleteSession();
  const confirm = useConfirm();
  const [deletingId, setDeletingId] = useState<string | null>(null);

  const run = async (s: WASession, action: SessionAction): Promise<void> => {
    const name = sessionName(s);
    if (action === "logout") {
      const ok = await confirm({
        title: <>Log out “{name}”?</>,
        description: `WhatsApp unlinks this device from ${
          formatPhone(s.phoneNumber) ?? "the phone"
        }. To use the number here again, you'll link it with a new QR code.`,
        confirmLabel: "Log out",
        destructive: true,
      });
      if (!ok) return;
    }
    lifecycle.mutate(
      { sessionId: s.id, action },
      {
        onError: (err) => toast.error(errMessage(err, `Couldn't ${action} ${name}`)),
        onSuccess: () => toast.success(ACTION_TOAST[action](name)),
      },
    );
  };

  const remove = async (
    s: WASession,
    { onDeleted }: { onDeleted?: () => void } = {},
  ): Promise<void> => {
    const name = sessionName(s);
    const paired = Boolean(s.waJid);
    const ok = await confirm({
      title: <>Delete “{name}”?</>,
      description: paired
        ? `${formatPhone(s.phoneNumber) ?? "A number"} is still linked. We'll log it out of WhatsApp first, then delete the session. This can't be undone.`
        : "The session and its settings are removed. This can't be undone.",
      confirmLabel: paired ? "Log out and delete" : "Delete session",
      destructive: true,
    });
    if (!ok) return;

    setDeletingId(s.id);
    try {
      if (paired) {
        try {
          await lifecycle.mutateAsync({ sessionId: s.id, action: "logout" });
        } catch (err) {
          const force = await confirm({
            title: "Couldn't log out",
            description: `${errMessage(err, "The logout request failed.")} Delete the session anyway? The phone may keep showing it under Linked devices until you remove it there.`,
            confirmLabel: "Delete anyway",
            destructive: true,
          });
          if (!force) return;
        }
      }
      // Right after a logout the gateway is still tearing the runtime down and
      // can answer DELETE with a transient not_found; retry briefly.
      for (let attempt = 0; ; attempt++) {
        try {
          await del.mutateAsync({ sessionId: s.id });
          break;
        } catch (err) {
          const transient = paired && isApiError(err) && err.code === "not_found";
          if (!transient || attempt >= 5) {
            if (transient) {
              throw new Error(
                `${name} is still logging out. Try deleting it again in a minute.`,
              );
            }
            throw err;
          }
          await new Promise((r) => setTimeout(r, 1000 * 2 ** Math.min(attempt, 3)));
        }
      }
      toast.success(`Deleted ${name}`);
      onDeleted?.();
    } catch (err) {
      toast.error(errMessage(err, `Couldn't delete ${name}`));
    } finally {
      setDeletingId(null);
    }
  };

  return {
    run,
    remove,
    /** The action in flight for `sessionId`, if any. */
    pendingFor: (sessionId: string): SessionAction | "delete" | undefined =>
      deletingId === sessionId
        ? "delete"
        : lifecycle.isPending && lifecycle.variables?.sessionId === sessionId
          ? lifecycle.variables.action
          : undefined,
  };
}

/**
 * The lifecycle controls for one session: the one primary action that applies
 * (Start when stopped, Stop when running) plus a "…" menu with Restart,
 * Log out and Delete.
 */
export function SessionActions({
  session,
  actions,
  onDeleted,
  showPrimary = true,
  size = "sm",
}: {
  session: WASession;
  actions: ReturnType<typeof useSessionActions>;
  onDeleted?: () => void;
  /** Render the Start/Stop button next to the menu. Default true. */
  showPrimary?: boolean;
  size?: "sm" | "default";
}) {
  const pending = actions.pendingFor(session.id);
  const busy = Boolean(pending);
  const running = isRunning(session.status);
  const paired = Boolean(session.waJid);

  return (
    <div className="flex items-center gap-1.5">
      {showPrimary &&
        (running ? (
          <Button
            variant="outline"
            size={size}
            disabled={busy}
            onClick={() => void actions.run(session, "stop")}
          >
            <SquareIcon aria-hidden data-icon="inline-start" />
            {pending === "stop" ? "Stopping…" : "Stop"}
          </Button>
        ) : (
          <Button
            variant="outline"
            size={size}
            disabled={busy}
            onClick={() => void actions.run(session, "start")}
          >
            <PlayIcon aria-hidden data-icon="inline-start" />
            {pending === "start" ? "Starting…" : "Start"}
          </Button>
        ))}
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button
            variant="ghost"
            size={size === "sm" ? "icon-sm" : "icon"}
            disabled={busy}
            aria-label={`More actions for ${sessionName(session)}`}
          >
            <MoreHorizontalIcon aria-hidden />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="min-w-44">
          {!showPrimary && (
            <DropdownMenuItem
              onSelect={() => void actions.run(session, running ? "stop" : "start")}
            >
              {running ? <SquareIcon aria-hidden /> : <PlayIcon aria-hidden />}
              {running ? "Stop" : "Start"}
            </DropdownMenuItem>
          )}
          <DropdownMenuItem onSelect={() => void actions.run(session, "restart")}>
            <RotateCwIcon aria-hidden />
            Restart
          </DropdownMenuItem>
          {paired && (
            <DropdownMenuItem
              variant="destructive"
              onSelect={() => void actions.run(session, "logout")}
            >
              <LogOutIcon aria-hidden />
              Log out
            </DropdownMenuItem>
          )}
          <DropdownMenuSeparator />
          <DropdownMenuItem
            variant="destructive"
            onSelect={() => void actions.remove(session, { onDeleted })}
          >
            <Trash2Icon aria-hidden />
            {paired ? "Log out and delete…" : "Delete session…"}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  );
}

// --- QR image ---------------------------------------------------------------
//
// The v2 QR route returns JSON only (the raw code string), so encode it
// client-side with the same `uqr` renderer the OAuth consent page uses: no
// fetch, no Bearer plumbing, and the image rotates with the code automatically.

export function QrImage({
  code,
  hint = true,
}: {
  code: string | undefined;
  /** Show the "how to scan" line under the code. Default true. */
  hint?: boolean;
}) {
  if (!code) {
    return (
      <p className="py-6 text-center text-sm text-muted-foreground">
        Waiting for a QR code…
      </p>
    );
  }

  return (
    <div className="flex flex-col items-center gap-3">
      <QrCode
        value={code}
        size={224}
        className="rounded-xl border shadow-none ring-0"
      />
      {hint && (
        <p className="text-center text-xs text-muted-foreground">
          Open WhatsApp → Linked devices → Link a device, then scan. The code
          refreshes automatically.
        </p>
      )}
    </div>
  );
}
