// Local UI helpers shared across the user-panel surface (sessions/keys/webhooks).
// Surface-local on purpose — see sharedGaps in the surface report; the verify
// phase may hoist these into a shared module if other surfaces need them.

import { useCallback, useState } from "react";
import { CheckIcon, CopyIcon } from "lucide-react";
import { Button } from "~/components/ui/button";
import { Badge } from "~/components/ui/badge";
import type { SessionStatus } from "~/lib/api/types";
import { cn } from "~/lib/utils";

/** Format an epoch-millis timestamp into a short, locale-aware label. */
export function formatTimestamp(ms: number | undefined): string {
  if (!ms) return "—";
  // The API emits int64 epoch millis; guard against accidental seconds.
  const normalized = ms < 1e12 ? ms * 1000 : ms;
  const d = new Date(normalized);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString();
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

/** A copy-to-clipboard button with transient "copied" feedback. */
export function CopyButton({
  value,
  label = "Copy",
  className,
}: {
  value: string;
  label?: string;
  className?: string;
}) {
  const [copied, setCopied] = useState(false);

  const onCopy = useCallback(() => {
    void navigator.clipboard
      .writeText(value)
      .then(() => {
        setCopied(true);
        window.setTimeout(() => setCopied(false), 1500);
      })
      .catch(() => setCopied(false));
  }, [value]);

  return (
    <Button
      type="button"
      size="sm"
      variant="outline"
      onClick={onCopy}
      className={cn("gap-1.5", className)}
      aria-label={label}
    >
      {copied ? (
        <CheckIcon className="size-4" aria-hidden />
      ) : (
        <CopyIcon className="size-4" aria-hidden />
      )}
      {copied ? "Copied" : label}
    </Button>
  );
}

/** Inline status badge with a humanized label. */
export function SessionStatusBadge({
  status,
}: {
  status: SessionStatus | undefined;
}) {
  return (
    <Badge variant={statusVariant(status)}>
      {(status ?? "unknown").replace(/_/g, " ")}
    </Badge>
  );
}
