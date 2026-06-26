// A single message bubble for the read-only viewer timeline. Renders text,
// poll, location and contact messages specially; media types show a graceful
// "not downloaded in v1" placeholder (media download is 501 in the API).

import {
  CheckCheck,
  Check,
  Clock,
  AlertCircle,
  MapPin,
  ListChecks,
  Contact as ContactIcon,
  FileWarning,
} from "lucide-react";
import type { Message, MessageStatus } from "~/lib/api/types";
import { cn } from "~/lib/utils";
import { formatTimestamp, parseMessage, type ParsedMessage } from "./viewer-ui";

export function MessageBubble({ message }: { message: Message }) {
  const outgoing = message.direction === "out";
  const parsed = parseMessage(message);

  return (
    <div
      className={cn(
        "flex w-full",
        outgoing ? "justify-end" : "justify-start",
      )}
    >
      <div
        className={cn(
          "max-w-[78%] rounded-2xl px-3 py-2 text-sm shadow-sm",
          outgoing
            ? "rounded-br-sm bg-primary text-primary-foreground"
            : "rounded-bl-sm bg-muted text-foreground",
        )}
      >
        {!outgoing && message.senderJid ? (
          <div className="mb-0.5 text-xs font-medium opacity-70">
            {message.senderJid.replace(/@.*$/, "")}
          </div>
        ) : null}

        <MessageContent parsed={parsed} outgoing={outgoing} />

        <div
          className={cn(
            "mt-1 flex items-center justify-end gap-1 text-[10px]",
            outgoing ? "text-primary-foreground/70" : "text-muted-foreground",
          )}
        >
          <span>{formatTimestamp(message.timestamp)}</span>
          {outgoing ? <StatusIcon status={message.status} /> : null}
        </div>
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
  switch (status) {
    case "pending":
      return <Clock className="size-3" aria-label="Pending" />;
    case "sent":
      return <Check className="size-3" aria-label="Sent" />;
    case "delivered":
      return <CheckCheck className="size-3" aria-label="Delivered" />;
    case "read":
    case "played":
      return (
        <CheckCheck className="size-3 text-sky-300" aria-label="Read" />
      );
    case "failed":
      return (
        <AlertCircle className="size-3 text-destructive" aria-label="Failed" />
      );
    default:
      return null;
  }
}
