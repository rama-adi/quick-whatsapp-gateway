// The one session-status vocabulary: human labels, colors and a one-line
// meaning (from guides/connect-a-number) for every WhatsApp session status.
// Use <SessionStatusBadge> everywhere a session status is shown.

import { Badge } from "~/components/ui/badge";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "~/components/ui/tooltip";
import type { SessionStatus } from "~/lib/api/types";
import { cn } from "~/lib/utils";

interface StatusMeta {
  label: string;
  description: string;
  /** Badge surface + text color. */
  tone: string;
  /** Leading dot color (+ pulse for transitional states). */
  dot: string;
}

/** Label, meaning and color for each session status. */
export const SESSION_STATUS: Record<SessionStatus, StatusMeta> = {
  working: {
    label: "Connected",
    description: "Linked and live — ready to send and receive.",
    tone: "bg-emerald-500/12 text-emerald-700 dark:text-emerald-400",
    dot: "bg-emerald-500",
  },
  starting: {
    label: "Connecting",
    description: "Connecting to WhatsApp.",
    tone: "bg-amber-500/12 text-amber-700 dark:text-amber-400",
    dot: "bg-amber-500 animate-pulse",
  },
  scan_qr_code: {
    label: "Waiting for QR scan",
    description: "Waiting for you to scan the QR code or enter the pairing code.",
    tone: "bg-sky-500/12 text-sky-700 dark:text-sky-400",
    dot: "bg-sky-500 animate-pulse",
  },
  stopped: {
    label: "Stopped",
    description: "Stopped on purpose; reconnects when you start it.",
    tone: "bg-muted text-muted-foreground",
    dot: "bg-muted-foreground/60",
  },
  logged_out: {
    label: "Logged out",
    description: "The link was removed (from the phone or by a ban); link the number again.",
    tone: "bg-muted text-muted-foreground",
    dot: "bg-muted-foreground/60",
  },
  failed: {
    label: "Failed",
    description: "Connection failed; the gateway stopped retrying. Try restarting the session.",
    tone: "bg-destructive/10 text-destructive dark:bg-destructive/20",
    dot: "bg-destructive",
  },
};

/** Human label for a session status ("scan_qr_code" → "Waiting for QR scan"). */
export function sessionStatusLabel(status: SessionStatus | undefined | null): string {
  return status ? (SESSION_STATUS[status]?.label ?? status) : "Unknown";
}

/** Session status pill with a colored dot; hover shows what the status means. */
export function SessionStatusBadge({
  status,
  tooltip = true,
  className,
}: {
  status: SessionStatus | undefined | null;
  /** Show the meaning in a tooltip on hover. Default true. */
  tooltip?: boolean;
  className?: string;
}) {
  const meta = status ? SESSION_STATUS[status] : undefined;
  const badge = (
    <Badge
      variant={meta ? "default" : "outline"}
      className={cn("gap-1.5", meta?.tone, className)}
      aria-label={`Status: ${meta?.label ?? "Unknown"}`}
    >
      <span
        aria-hidden
        className={cn("size-1.5 rounded-full", meta?.dot ?? "bg-muted-foreground/60")}
      />
      {meta?.label ?? (status || "Unknown")}
    </Badge>
  );
  if (!tooltip || !meta) return badge;
  return (
    <TooltipProvider delayDuration={300}>
      <Tooltip>
        <TooltipTrigger asChild>{badge}</TooltipTrigger>
        <TooltipContent className="max-w-64">{meta.description}</TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
}
