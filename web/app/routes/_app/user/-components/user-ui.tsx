// Surface-local UI helpers for the user panel (sessions / keys / webhooks).
// Ported from the v1 user/_ui.tsx (tag mvp-v1) and re-fitted to TanStack
// Start. Colocated under -components/ (the leading "-" excludes it from
// file-based route generation) on purpose — the Verify stage may hoist these
// into a shared module if the admin/viewer/contacts surfaces need them too.

import { useCallback, useState } from "react";
import { CheckIcon, CopyIcon } from "lucide-react";
import { Button } from "~/components/ui/button";
import { Badge } from "~/components/ui/badge";
import type { SessionStatus } from "~/lib/api/types";
import { QrCode } from "~/routes/-oauth/QrCode";
import { cn } from "~/lib/utils";

/** Format an epoch-millis timestamp into a short, locale-aware label. */
export function formatTimestamp(ms: number | undefined): string {
  if (!ms) return "—";
  // The API emits int64 epoch millis; guard against accidental seconds.
  const normalized = ms < 1e12 ? ms * 1000 : ms;
  const d = new Date(normalized);
  if (Number.isNaN(d.getTime())) return "—";
  return new Intl.DateTimeFormat("en", {
    dateStyle: "medium",
    timeStyle: "short",
    timeZone: "UTC",
  }).format(d);
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

// --- QR image ---------------------------------------------------------------
//
// v1 rendered the QR as <img src={…?format=image}> served by the gateway; the
// v2 QR route returns JSON only (the raw code string). Since `code` (from the
// auth.qr event / GET /qr) is already in hand, encode it client-side with the
// same `uqr` renderer the OAuth consent page uses — no fetch, no Bearer
// plumbing, and the image rotates with the code automatically.

export function QrImage({ code }: { code: string | undefined }) {
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
        className="rounded-md border shadow-none ring-0"
      />
      <p className="text-center text-xs text-muted-foreground">
        Open WhatsApp → Linked devices → Link a device, then scan. The code
        refreshes automatically.
      </p>
    </div>
  );
}
