// One message in the viewer timeline, built on the shadcn chat primitives.
// Outgoing (direction:"out") messages align end with the primary Bubble variant;
// incoming align start and are tinted. The Message wraps a header (sender name),
// the Bubble/BubbleContent body (text/poll/location/contact, or an Attachment
// placeholder for media — download is 501 in v1), a footer (timestamp + ack
// ticks), and BubbleReactions. Reply previews, reactions and the "edited" mark
// come from the body JSON via parseExtras — all optional.

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
import type { ReactNode } from "react";
import type { Message, MessageStatus } from "~/lib/api/types";
import { cn } from "~/lib/utils";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "~/components/ui/tooltip";
import {
  Message as MessageRow,
  MessageContent as MessageContentSlot,
  MessageHeader,
  MessageFooter,
} from "~/components/ui/message";
import {
  Bubble,
  BubbleContent,
  BubbleReactions,
} from "~/components/ui/bubble";
import {
  Attachment,
  AttachmentMedia,
  AttachmentContent,
  AttachmentTitle,
  AttachmentDescription,
} from "~/components/ui/attachment";
import {
  formatTimestamp,
  parseMessage,
  parseExtras,
  type ParsedMessage,
} from "./-viewer-ui";

/** Best display label for an incoming message's sender. Prefers the resolved
 * senderName; if that's missing or is itself a raw JID/LID, falls back to the
 * sender id with its server suffix (and device part) stripped. */
function senderLabel(m: Message): string | undefined {
  if (m.senderName && !m.senderName.includes("@")) return m.senderName;
  const id = m.senderJid ?? m.senderLid ?? m.senderName;
  return id ? id.replace(/[:@].*$/, "") : undefined;
}

/** Render a body, turning "@<number>" tokens into "@<name>" chips when the
 * mention resolves to a known display name (mentionNames is keyed by the token's
 * user-part). Unresolved mentions are left as-is. */
function renderWithMentions(
  text: string,
  mentionNames?: Record<string, string>,
): ReactNode {
  if (!mentionNames || Object.keys(mentionNames).length === 0) return text;
  const parts: ReactNode[] = [];
  let last = 0;
  const re = /@(\w+)/g;
  let m: RegExpExecArray | null;
  let key = 0;
  while ((m = re.exec(text)) !== null) {
    const name = mentionNames[m[1] ?? ""];
    if (!name) continue;
    if (m.index > last) parts.push(text.slice(last, m.index));
    parts.push(
      <span key={key++} className="font-medium text-primary">
        @{name}
      </span>,
    );
    last = m.index + m[0].length;
  }
  if (parts.length === 0) return text;
  if (last < text.length) parts.push(text.slice(last));
  return parts;
}

const STATUS_LABEL: Record<MessageStatus, string> = {
  pending: "Pending",
  sent: "Sent",
  delivered: "Delivered",
  read: "Read",
  played: "Played",
  failed: "Failed",
};

export function MessageBubble({
  message,
  showSender = true,
}: {
  message: Message;
  /** First message of a same-sender run shows the header; later ones don't. */
  showSender?: boolean;
}) {
  const parsed = parseMessage(message);

  // System/protocol rows (E2E-encryption notices, ephemeral settings, sender-key
  // distribution, …) carry no human-readable body — their stored body is raw
  // event JSON. The gateway drops these going forward; legacy rows already in the
  // DB render as nothing so they never show JSON garbage in the timeline.
  if (parsed.kind === "system") return null;

  const outgoing = message.direction === "out";
  const extras = parseExtras(message);
  const sender = !outgoing && showSender ? senderLabel(message) : undefined;
  const align = outgoing ? "end" : "start";
  const hasReactions = extras.reactions.length > 0;

  return (
    <MessageRow align={align}>
      <MessageContentSlot>
        {sender ? (
          <MessageHeader>
            <span className="truncate">{sender}</span>
          </MessageHeader>
        ) : null}

        <Bubble
          variant={outgoing ? "default" : "tinted"}
          align={align}
          className={cn(hasReactions && "mb-3")}
        >
          <BubbleContent>
            {extras.quoted ? (
              <QuotedPreview quoted={extras.quoted} outgoing={outgoing} />
            ) : null}

            <MessageBody
              parsed={parsed}
              outgoing={outgoing}
              mentionNames={message.mentionNames}
            />
          </BubbleContent>

          {hasReactions ? (
            <BubbleReactions side="bottom" align={align}>
              {extras.reactions.map((emoji, i) => (
                <span key={`${i}-${emoji}`} className="leading-none">
                  {emoji}
                </span>
              ))}
            </BubbleReactions>
          ) : null}
        </Bubble>

        <MessageFooter className="gap-1">
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
        </MessageFooter>
      </MessageContentSlot>
    </MessageRow>
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

function MessageBody({
  parsed,
  outgoing,
  mentionNames,
}: {
  parsed: ParsedMessage;
  outgoing: boolean;
  mentionNames?: Record<string, string>;
}) {
  switch (parsed.kind) {
    case "text":
      return parsed.text ? (
        <p className="whitespace-pre-wrap break-words">
          {renderWithMentions(parsed.text, mentionNames)}
        </p>
      ) : (
        <p className="italic opacity-60">(empty message)</p>
      );

    case "media":
      // Media isn't downloadable in v1 (the API returns 501), so render an
      // Attachment in its unavailable/placeholder state (state="idle" → dashed
      // border) rather than an actual media tile.
      return (
        <div className="space-y-1">
          <Attachment
            state="idle"
            className="border-current/25 bg-transparent text-inherit"
          >
            <AttachmentMedia variant="icon" className="bg-current/10">
              <FileWarning aria-hidden />
            </AttachmentMedia>
            <AttachmentContent>
              <AttachmentTitle className="capitalize">
                {parsed.mediaType}
              </AttachmentTitle>
              <AttachmentDescription className="text-current/70">
                Media not downloaded — not available in v1.
              </AttachmentDescription>
            </AttachmentContent>
          </Attachment>
          {parsed.caption ? (
            <p className="whitespace-pre-wrap break-words">{parsed.caption}</p>
          ) : null}
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

    case "system":
      // Handled by the centered-notice early return in MessageBubble; never
      // reaches the bubble body. Render nothing as a safety net.
      return null;

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
        return <CheckCheck className="size-3 text-sky-500" aria-hidden />;
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
