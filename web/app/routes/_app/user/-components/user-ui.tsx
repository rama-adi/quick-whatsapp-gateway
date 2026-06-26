// Surface-local UI helpers for the user panel (sessions / keys / webhooks).
// Ported from the v1 app/_v1-surfaces/user/_ui.tsx and re-fitted to TanStack
// Start. Colocated under -components/ (the leading "-" excludes it from
// file-based route generation) on purpose — the Verify stage may hoist these
// into a shared module if the admin/viewer/contacts surfaces need them too.

import { useCallback, useEffect, useRef, useState } from "react";
import { CheckIcon, CopyIcon, RefreshCwIcon } from "lucide-react";
import { Button } from "~/components/ui/button";
import { Badge } from "~/components/ui/badge";
import { Skeleton } from "~/components/ui/skeleton";
import type { SessionStatus } from "~/lib/api/types";
import { apiUrl } from "~/lib/api/client";
import { getGatewayToken } from "~/lib/api/token-provider";
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

// --- QR image (cross-origin Bearer) ---------------------------------------
//
// v1 rendered the QR as <img src={…?format=image}> relying on the SAME-ORIGIN
// cookie to authenticate the request. In v2 the gateway is a DIFFERENT origin
// and authenticates by `Authorization: Bearer <jwt>` (§4/§12) — an <img> tag
// cannot attach that header. So we fetch the PNG as a blob WITH the gateway
// Bearer (the same token provider the JSON client uses) and render it via an
// object URL. `code` (the live QR string from the auth.qr event / GET /qr)
// keys the fetch so the image refreshes when the code rotates.

export function QrImage({
  sessionId,
  code,
}: {
  sessionId: string;
  code: string | undefined;
}) {
  const [src, setSrc] = useState<string | null>(null);
  const [error, setError] = useState(false);
  const [nonce, setNonce] = useState(0);
  const objectUrl = useRef<string | null>(null);

  useEffect(() => {
    if (!code) {
      setSrc(null);
      return;
    }
    let cancelled = false;
    setError(false);
    const url =
      `${apiUrl(`/sessions/${encodeURIComponent(sessionId)}/qr`)}` +
      `?format=image&v=${encodeURIComponent(code)}`;

    void (async () => {
      try {
        const token = await getGatewayToken();
        const res = await fetch(url, {
          headers: token ? { Authorization: `Bearer ${token}` } : undefined,
        });
        if (!res.ok) throw new Error(`qr ${res.status}`);
        const blob = await res.blob();
        if (cancelled) return;
        const next = URL.createObjectURL(blob);
        if (objectUrl.current) URL.revokeObjectURL(objectUrl.current);
        objectUrl.current = next;
        setSrc(next);
      } catch {
        if (!cancelled) setError(true);
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [sessionId, code, nonce]);

  useEffect(
    () => () => {
      if (objectUrl.current) URL.revokeObjectURL(objectUrl.current);
    },
    [],
  );

  if (!code) {
    return (
      <p className="py-6 text-center text-sm text-muted-foreground">
        Waiting for a QR code…
      </p>
    );
  }

  if (error || !src) {
    return error ? (
      <div className="flex flex-col items-center gap-3 py-6 text-center">
        <p className="text-sm text-muted-foreground">Could not load the QR image.</p>
        <Button
          variant="outline"
          size="sm"
          className="gap-1.5"
          onClick={() => setNonce((n) => n + 1)}
        >
          <RefreshCwIcon className="size-4" aria-hidden />
          Retry
        </Button>
      </div>
    ) : (
      <Skeleton className="mx-auto size-56" />
    );
  }

  return (
    <div className="flex flex-col items-center gap-3">
      <img
        src={src}
        alt="WhatsApp pairing QR code"
        width={224}
        height={224}
        className="size-56 rounded-md border bg-white p-2"
      />
      <p className="text-center text-xs text-muted-foreground">
        Open WhatsApp → Linked devices → Link a device, then scan. The code
        refreshes automatically.
      </p>
      <Button
        variant="ghost"
        size="sm"
        className="gap-1.5"
        onClick={() => setNonce((n) => n + 1)}
      >
        <RefreshCwIcon className="size-4" aria-hidden />
        Refresh
      </Button>
    </div>
  );
}
