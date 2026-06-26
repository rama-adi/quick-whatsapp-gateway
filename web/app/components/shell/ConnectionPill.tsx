// Live connection indicator bound to the event-stream status.
// FROZEN — owned by the foundation agent.

import { Wifi, WifiOff, Loader2, RefreshCw } from "lucide-react";
import { Badge } from "~/components/ui/badge";
import { cn } from "~/lib/utils";
import { useEventStream } from "~/lib/events/useEventStream";

export function ConnectionPill({ className }: { className?: string }) {
  const { status, reconnectNow } = useEventStream();

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
