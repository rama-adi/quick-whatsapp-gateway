// Live connection indicator bound to the event-stream status.
// Owned by the foundation agent.
//
// Optional, reusable surface indicator — drop <ConnectionPill/> into any surface
// that wants to show event-stream liveness. It is NOT mounted in the app top bar
// (that slot now carries the org switcher). The stream is page-scoped
// (reference-counted opt-in), so this pill renders nothing on surfaces that
// haven't requested it — it only reflects liveness where a connection exists.

import { Wifi, WifiOff, Loader2, RefreshCw } from "lucide-react";
import { Badge } from "~/components/ui/badge";
import { cn } from "~/lib/utils";
import { useEventStream } from "~/lib/events/useEventStream";

export function ConnectionPill({ className }: { className?: string }) {
  const { status, reconnectNow, active } = useEventStream();

  // No surface on this page requested the stream — nothing to indicate.
  if (!active) return null;

  const label =
    status === "open"
      ? "Live"
      : status === "connecting"
        ? "Connecting"
        : status === "reconnecting"
          ? "Reconnecting"
          : status === "polling"
            ? "Degraded"
            : status === "closed"
              ? "Offline"
              : "Idle";

  const Icon =
    status === "open"
      ? Wifi
      : status === "polling" || status === "closed"
        ? WifiOff
        : status === "connecting" || status === "reconnecting"
          ? Loader2
          : RefreshCw;

  const variant =
    status === "open" ? "default" : status === "closed" ? "destructive" : "secondary";

  const spin = status === "connecting" || status === "reconnecting";

  return (
    <button
      type="button"
      onClick={() => reconnectNow()}
      title="Click to reconnect the event stream"
      className={cn("inline-flex items-center", className)}
    >
      <Badge variant={variant} className="gap-1.5">
        <Icon className={cn("size-3", spin && "animate-spin")} aria-hidden />
        {label}
      </Badge>
    </button>
  );
}
