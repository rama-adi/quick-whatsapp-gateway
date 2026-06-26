// One message in the viewer timeline. Outgoing (from_me) bubbles sit right and
// tinted with the primary color; incoming bubbles sit left and muted. Renders
// text/poll/location/contact specially; media types show a "not downloaded"
// placeholder (download is 501 in the API). Reply previews, reactions, an
// "edited" mark and the absolute send time (on hover) come from the body JSON
// via parseExtras — all optional.

import {
  CheckCheck,
  Check,
  Clock,
  AlertCircle,
  MapPin,
  ListChecks,
  Contact as ContactIcon,
  FileWarning,
  CornerUpLeft,
} from "lucide-react";
import type { Message, MessageStatus } from "~/lib/api/types";
import { cn } from "~/lib/utils";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "~/components/ui/tooltip";
import {
  formatTimestamp,
  parseMessage,
  parseExtras,
  type ParsedMessage,
} from "./-viewer-ui";

const STATUS_LABEL: Record<MessageStatus, string> = {
  pending: "Pending",
  sent: "Sent",
  delivered: "Delivered",
  read: "Read",
  played: "Played",
  failed: "Failed",
};

export function MessageBubble({ message }: { message: Message }) {
  const outgoing = message.direction === "out";
  const parsed = parseMessage(message);
  const extras = parseExtras(message);
  const sender = !outgoing ? message.senderJid?.replace(/@.*$/, "") : undefined;

  return (
    <div className={cn("flex w-full", outgoing ? "justify-end" : "justify-start")}>
      <div className="flex max-w-[78%] flex-col gap-1">
        <div
          className={cn(
            "rounded-2xl px-3 py-2 text-sm shadow-sm",
            outgoing
              ? "rounded-br-sm bg-primary text-primary-foreground"
              : "rounded-bl-sm bg-muted text-foreground",
          )}
        >
          {sender ? (
            <div className="mb-0.5 text-xs font-medium opacity-70">{sender}</div>
          ) : null}

          {extras.quoted ? (
            <QuotedPreview quoted={extras.quoted} outgoing={outgoing} />
          ) : null}

          <MessageContent parsed={parsed} outgoing={outgoing} />

          <div
            className={cn(
              "mt-1 flex items-center justify-end gap-1 text-[10px]",
              outgoing ? "text-primary-foreground/70" : "text-muted-foreground",
            )}
          >
            {extras.edited ? <span className="italic">edited</span> : null}
            <Tooltip>
              <TooltipTrigger asChild>
                <button
                  type="button"
                  className="cursor-default rounded-sm outline-none focus-visible:ring-1 focus-visible:ring-ring"
                  aria-label={
                    message.timestamp
                      ? `Sent ${new Date(message.timestamp).toLocaleString()}`
                      : "Send time unknown"
                  }
                >
                  <time
                    dateTime={
                      message.timestamp
                        ? new Date(message.timestamp).toISOString()
                        : undefined
                    }
                  >
                    {formatTimestamp(message.timestamp)}
                  </time>
                </button>
              </TooltipTrigger>
              <TooltipContent>
                {message.timestamp
                  ? new Date(message.timestamp).toLocaleString()
                  : "Unknown time"}
              </TooltipContent>
            </Tooltip>
            {outgoing ? <StatusIcon status={message.status} /> : null}
          </div>
        </div>

        {extras.reactions.length > 0 ? (
          <div
            className={cn(
              "flex flex-wrap gap-1",
              outgoing ? "justify-end" : "justify-start",
            )}
          >
            {extras.reactions.map((emoji, i) => (
              <span
                key={`${i}-${emoji}`}
                className="rounded-full border bg-background px-1.5 py-0.5 text-xs leading-none shadow-sm"
              >
                {emoji}
              </span>
            ))}
          </div>
        ) : null}
      </div>
    </div>
  );
}

function QuotedPreview({
  quoted,
  outgoing,
}: {
  quoted: NonNullable<ReturnType<typeof parseExtras>["quoted"]>;
  outgoing: boolean;
}) {
  return (
    <div
      className={cn(
        "mb-1 flex items-start gap-1.5 rounded-md border-l-2 px-2 py-1 text-xs",
        outgoing
          ? "border-primary-foreground/50 bg-primary-foreground/10"
          : "border-primary/50 bg-background/60",
      )}
    >
      <CornerUpLeft className="mt-0.5 size-3 shrink-0 opacity-70" aria-hidden />
      <div className="min-w-0">
        {quoted.author ? (
          <p className="font-medium opacity-80">{quoted.author}</p>
        ) : null}
        <p className="truncate opacity-70">{quoted.preview}</p>
      </div>
    </div>
  );
}

function MessageContent({
  parsed,
  outgoing,
}: {
  parsed: ParsedMessage;
  outgoing: boolean;
}) {
  switch (parsed.kind) {
    case "text":
      return parsed.text ? (
        <p className="whitespace-pre-wrap break-words">{parsed.text}</p>
      ) : (
        <p className="italic opacity-60">(empty message)</p>
      );

    case "media":
      return (
        <div
          className={cn(
            "flex items-start gap-2 rounded-md border border-dashed p-2",
            outgoing ? "border-primary-foreground/30" : "border-border",
          )}
        >
          <FileWarning className="mt-0.5 size-4 shrink-0 opacity-70" aria-hidden />
          <div className="space-y-0.5">
            <p className="font-medium capitalize">{parsed.mediaType}</p>
            <p className="text-xs opacity-70">
              Media not downloaded — not available in v1.
            </p>
            {parsed.caption ? (
              <p className="whitespace-pre-wrap break-words pt-1">
                {parsed.caption}
              </p>
            ) : null}
          </div>
        </div>
      );

    case "poll":
      return (
        <div className="space-y-1">
          <div className="flex items-center gap-1.5 font-medium">
            <ListChecks className="size-4 shrink-0 opacity-80" aria-hidden />
            <span>{parsed.name || "Poll"}</span>
          </div>
          {parsed.options.length > 0 ? (
            <ul className="space-y-1 pt-1">
              {parsed.options.map((opt, i) => (
                <li
                  key={`${i}-${opt}`}
                  className={cn(
                    "rounded-md border px-2 py-1 text-xs",
                    outgoing
                      ? "border-primary-foreground/30"
                      : "border-border",
                  )}
                >
                  {opt}
                </li>
              ))}
            </ul>
          ) : (
            <p className="text-xs opacity-70">No options available.</p>
          )}
        </div>
      );

    case "location":
      return (
        <div className="space-y-1">
          <div className="flex items-center gap-1.5 font-medium">
            <MapPin className="size-4 shrink-0 opacity-80" aria-hidden />
            <span>{parsed.name || "Location"}</span>
          </div>
          {typeof parsed.latitude === "number" &&
          typeof parsed.longitude === "number" ? (
            <a
              href={`https://www.openstreetmap.org/?mlat=${parsed.latitude}&mlon=${parsed.longitude}#map=16/${parsed.latitude}/${parsed.longitude}`}
              target="_blank"
              rel="noopener noreferrer"
              className="text-xs underline opacity-80 hover:opacity-100"
            >
              {parsed.latitude.toFixed(5)}, {parsed.longitude.toFixed(5)}
            </a>
          ) : (
            <p className="text-xs opacity-70">Coordinates unavailable.</p>
          )}
        </div>
      );

    case "contact":
      return (
        <div className="flex items-center gap-2">
          <ContactIcon className="size-4 shrink-0 opacity-80" aria-hidden />
          <div className="space-y-0.5">
            <p className="font-medium">{parsed.name || "Shared contact"}</p>
            {parsed.phone ? (
              <p className="text-xs opacity-70">{parsed.phone}</p>
            ) : null}
          </div>
        </div>
      );

    case "unknown":
    default:
      return (
        <div className="space-y-0.5">
          <p className="italic opacity-70">
            Unsupported message{parsed.type ? ` (${parsed.type})` : ""}.
          </p>
          {parsed.text ? (
            <p className="whitespace-pre-wrap break-words">{parsed.text}</p>
          ) : null}
        </div>
      );
  }
}

function StatusIcon({ status }: { status?: MessageStatus }) {
  if (!status) return null;
  const label = STATUS_LABEL[status];
  const icon = (() => {
    switch (status) {
      case "pending":
        return <Clock className="size-3" aria-hidden />;
      case "sent":
        return <Check className="size-3" aria-hidden />;
      case "delivered":
        return <CheckCheck className="size-3" aria-hidden />;
      case "read":
      case "played":
        return <CheckCheck className="size-3 text-sky-300" aria-hidden />;
      case "failed":
        return <AlertCircle className="size-3 text-destructive" aria-hidden />;
    }
  })();
  return (
    <span role="img" aria-label={label}>
      {icon}
    </span>
  );
}
