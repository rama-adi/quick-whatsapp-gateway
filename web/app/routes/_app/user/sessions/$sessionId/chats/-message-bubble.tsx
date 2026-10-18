// One message in the chat timeline, built on the shadcn chat primitives.
// Outgoing (direction:"out") messages align end with the soft emerald
// "outgoing" Bubble; incoming align start on a raised white "incoming" Bubble.
// Inside the bubble: the sender (group chats, first of a run), a reply preview,
// the body (text/media/poll/location/contact), and a meta row (edited, time,
// delivery ticks) that sits on the last text line when it fits. Reactions hang
// off the bubble edge. A hover (or always-visible on touch) "…" menu offers
// Reply, Copy text and Copy message ID.

import {
  CheckCheck,
  Check,
  Clock,
  AlertCircle,
  MapPin,
  ListChecks,
  Contact as ContactIcon,
  FileText,
  FileWarning,
  CornerUpLeft,
  Copy,
  ExternalLink,
  Hash,
  MoreHorizontal,
  Phone,
  Reply,
} from "lucide-react";
import { useState, type ReactNode } from "react";
import { Link } from "@tanstack/react-router";
import { toast } from "sonner";
import type { Message, MessageStatus } from "~/lib/api/types";
import { cn } from "~/lib/utils";
import { copyText } from "~/components/copyable";
import { Button } from "~/components/ui/button";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "~/components/ui/tooltip";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "~/components/ui/dropdown-menu";
import { Message as MessageRow } from "~/components/ui/message";
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
  prettyName,
  parseExtras,
  type ParsedMessage,
} from "./-viewer-ui";


/** Best display label for an incoming message's sender. Prefers the resolved
 * senderName; if that's missing or is itself a raw JID/LID, falls back to the
 * sender id with its server suffix (and device part) stripped. */
function senderLabel(m: Message): string | undefined {
  if (m.senderName && !m.senderName.includes("@")) return prettyName(m.senderName);
  const id = m.senderJid ?? m.senderLid ?? m.senderName;
  return id ? prettyName(id.replace(/[:@].*$/, "")) : undefined;
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
      <span key={key++} className="font-medium text-emerald-700 dark:text-emerald-400">
        @{name}
      </span>,
    );
    last = m.index + m[0].length;
  }
  if (parts.length === 0) return text;
  if (last < text.length) parts.push(text.slice(last));
  return parts;
}

function renderMarkdownText(
  text: string,
  mentionNames?: Record<string, string>,
): ReactNode {
  const lines = text.split("\n");
  return lines.map((line, lineIndex) => {
    const rendered = renderInlineMarkdown(line, mentionNames);
    const isQuote = line.startsWith("> ");
    const isList = /^[-*]\s+/.test(line);
    const isNumbered = /^\d+\.\s+/.test(line);
    const content = isQuote
      ? line.slice(2)
      : isList
        ? line.replace(/^[-*]\s+/, "")
        : isNumbered
          ? line.replace(/^\d+\.\s+/, "")
          : null;

    if (isQuote) {
      return (
        <blockquote
          key={lineIndex}
          className="my-1 border-l-2 border-current/35 pl-2 opacity-80"
        >
          {renderInlineMarkdown(content ?? "", mentionNames)}
        </blockquote>
      );
    }
    if (isList || isNumbered) {
      return (
        <div key={lineIndex} className="flex gap-2">
          <span className="mt-0.5 text-xs opacity-60">
            {isList ? "•" : line.match(/^\d+/)?.[0] ?? "1"}.
          </span>
          <span>{renderInlineMarkdown(content ?? "", mentionNames)}</span>
        </div>
      );
    }
    return (
      <span key={lineIndex}>
        {rendered}
        {lineIndex < lines.length - 1 ? <br /> : null}
      </span>
    );
  });
}

function renderInlineMarkdown(
  text: string,
  mentionNames?: Record<string, string>,
): ReactNode {
  const nodes: ReactNode[] = [];
  const token = /(\*\*[^*]+\*\*|__[^_]+__|\*[^*]+\*|_[^_]+_|`[^`]+`|\[[^\]]+\]\(https?:\/\/[^)]+\))/g;
  let last = 0;
  let match: RegExpExecArray | null;
  let key = 0;
  while ((match = token.exec(text)) !== null) {
    if (match.index > last) {
      nodes.push(renderWithMentions(text.slice(last, match.index), mentionNames));
    }
    const raw = match[0];
    if (raw.startsWith("**") || raw.startsWith("__")) {
      nodes.push(<strong key={key++}>{raw.slice(2, -2)}</strong>);
    } else if (raw.startsWith("*") || raw.startsWith("_")) {
      nodes.push(<em key={key++}>{raw.slice(1, -1)}</em>);
    } else if (raw.startsWith("`")) {
      nodes.push(
        <code key={key++} className="rounded bg-current/10 px-1 py-0.5 text-[0.92em]">
          {raw.slice(1, -1)}
        </code>,
      );
    } else {
      const link = /^\[([^\]]+)\]\((https?:\/\/[^)]+)\)$/.exec(raw);
      if (link) {
        nodes.push(
          <a
            key={key++}
            href={link[2]}
            target="_blank"
            rel="noopener noreferrer"
            className="inline-flex items-center gap-1 underline underline-offset-2"
          >
            {link[1]}
            <ExternalLink className="size-3" aria-hidden />
          </a>,
        );
      }
    }
    last = match.index + raw.length;
  }
  if (last < text.length) {
    nodes.push(renderWithMentions(text.slice(last), mentionNames));
  }
  return nodes.length > 0 ? nodes : renderWithMentions(text, mentionNames);
}


const STATUS_LABEL: Record<MessageStatus, string> = {
  pending: "Sending",
  sent: "Sent",
  delivered: "Delivered",
  read: "Read",
  played: "Played",
  failed: "Not sent",
};

export function MessageBubble({
  message,
  quotedMessage,
  showSender = false,
  tail = false,
  onReply,
  onVote,
}: {
  message: Message;
  quotedMessage?: Message;
  /** Group chats: the first message of a same-sender run shows the sender. */
  showSender?: boolean;
  /** Last message of a run: square off the corner nearest the sender's side. */
  tail?: boolean;
  onReply?: (message: Message) => void;
  onVote?: (message: Message, options: string[]) => void;
}) {
  const parsed = parseMessage(message);

  // System/protocol rows (E2E-encryption notices, ephemeral settings, sender-key
  // distribution, …) carry no human-readable body — their stored body is raw
  // event JSON. The gateway drops these going forward; legacy rows already in the
  // DB render as nothing so they never show JSON garbage in the timeline.
  if (parsed.kind === "system") return null;

  const outgoing = message.direction === "out";
  const extras = parseExtras(message);
  const quoted =
    extras.quoted ??
    (message.quotedMessageId
      ? {
          author: quotedMessage ? senderLabel(quotedMessage) : undefined,
          preview: quotedMessage
            ? messagePreview(quotedMessage)
            : "Earlier message",
        }
      : undefined);
  const sender = !outgoing && showSender ? senderLabel(message) : undefined;
  const align = outgoing ? "end" : "start";
  const hasReactions = !message.deleted && extras.reactions.length > 0;
  const copyable = copyableText(parsed);

  return (
    <MessageRow align={align}>
      <div
        className={cn(
          "flex max-w-[min(88%,40rem)] min-w-0 items-center gap-1",
          outgoing && "flex-row-reverse",
        )}
      >
        <Bubble
          variant={outgoing ? "outgoing" : "incoming"}
          align={align}
          className={cn("max-w-full", hasReactions && "mb-3")}
        >
          <BubbleContent
            className={cn(
              "rounded-2xl px-3 py-1.5",
              tail && (outgoing ? "rounded-br-md" : "rounded-bl-md"),
            )}
          >
            {sender ? (
              <p className="mb-0.5 truncate text-xs font-semibold text-emerald-700 dark:text-emerald-400">
                {sender}
              </p>
            ) : null}
            {quoted && !message.deleted ? <QuotedPreview quoted={quoted} /> : null}

            <div className="flex flex-wrap items-end gap-x-3 gap-y-0.5">
              <div className="max-w-full min-w-0">
                <MessageBody
                  parsed={parsed}
                  mentionNames={message.mentionNames}
                  message={message}
                  onVote={onVote}
                />
              </div>
              <MessageMeta message={message} outgoing={outgoing} edited={extras.edited} />
            </div>
          </BubbleContent>

          {hasReactions ? (
            <BubbleReactions
              side="bottom"
              align={align}
              className="bg-background shadow-xs ring-2 ring-border/40"
            >
              {extras.reactions.map((emoji, i) => (
                <span key={`${i}-${emoji}`} className="leading-none">
                  {emoji}
                </span>
              ))}
            </BubbleReactions>
          ) : null}
        </Bubble>

        {!message.deleted ? (
          <MessageActions
            message={message}
            align={align}
            copyable={copyable}
            onReply={onReply}
          />
        ) : null}
      </div>
    </MessageRow>
  );
}

function MessageMeta({
  message,
  outgoing,
  edited,
}: {
  message: Message;
  outgoing: boolean;
  edited: boolean;
}) {
  const failed = outgoing && message.status === "failed";
  const full = message.timestamp
    ? new Date(message.timestamp).toLocaleString()
    : "Unknown time";
  return (
    <span className="ml-auto inline-flex shrink-0 translate-y-0.5 items-center gap-1 text-[11px] leading-none text-muted-foreground tabular-nums">
      {edited && !message.deleted ? <span>edited</span> : null}
      <Tooltip>
        <TooltipTrigger asChild>
          <button
            type="button"
            className="cursor-default rounded-sm outline-none focus-visible:ring-1 focus-visible:ring-ring"
            aria-label={message.timestamp ? `Sent ${full}` : "Send time unknown"}
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
          {failed && message.error ? `${full} · ${message.error}` : full}
        </TooltipContent>
      </Tooltip>
      {outgoing ? <StatusIcon status={message.status} /> : null}
      {failed ? <span className="font-medium text-destructive">Not sent</span> : null}
    </span>
  );
}

function MessageActions({
  message,
  align,
  copyable,
  onReply,
}: {
  message: Message;
  align: "start" | "end";
  copyable: string | null;
  onReply?: (message: Message) => void;
}) {
  const id = message.waMessageId || message.id;
  const copy = (value: string, what: string) => {
    void copyText(value).then((ok) =>
      ok ? toast.success(`${what} copied`) : toast.error("Couldn't copy"),
    );
  };
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          type="button"
          variant="ghost"
          size="icon-xs"
          aria-label="Message actions"
          className="shrink-0 rounded-full text-muted-foreground opacity-0 transition-opacity duration-150 ease-soft group-hover/message:opacity-100 hover:bg-background hover:text-foreground focus-visible:opacity-100 data-[state=open]:opacity-100 pointer-coarse:opacity-60"
        >
          <MoreHorizontal aria-hidden />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align={align} className="w-44">
        {onReply ? (
          <DropdownMenuItem onClick={() => onReply(message)}>
            <Reply /> Reply
          </DropdownMenuItem>
        ) : null}
        {copyable ? (
          <DropdownMenuItem onClick={() => copy(copyable, "Text")}>
            <Copy /> Copy text
          </DropdownMenuItem>
        ) : null}
        {id && !id.startsWith("tmp_") ? (
          <DropdownMenuItem onClick={() => copy(id, "Message ID")}>
            <Hash /> Copy message ID
          </DropdownMenuItem>
        ) : null}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

function copyableText(parsed: ParsedMessage): string | null {
  if (parsed.kind === "text") return parsed.text || null;
  if (parsed.kind === "media") return parsed.caption || null;
  if (parsed.kind === "unknown") return parsed.text || null;
  return null;
}

function messagePreview(message: Message): string {
  if (message.deleted) return "This message was deleted";
  const body = message.body?.trim();
  if (body) return body.length > 160 ? `${body.slice(0, 160)}...` : body;
  return message.type ? `${message.type} message` : "Message";
}

function QuotedPreview({
  quoted,
}: {
  quoted: NonNullable<ReturnType<typeof parseExtras>["quoted"]>;
}) {
  return (
    <div className="mt-0.5 mb-1 flex items-start gap-1.5 rounded-lg border-l-2 border-emerald-600/60 bg-foreground/[0.04] px-2 py-1 text-xs dark:border-emerald-400/60 dark:bg-foreground/[0.06]">
      <CornerUpLeft className="mt-0.5 size-3 shrink-0 opacity-60" aria-hidden />
      <div className="min-w-0">
        {quoted.author ? (
          <p className="font-medium text-foreground/80">{quoted.author}</p>
        ) : null}
        <p className="line-clamp-2 text-muted-foreground">{quoted.preview}</p>
      </div>
    </div>
  );
}

function MessageBody({
  parsed,
  mentionNames,
  message,
  onVote,
}: {
  parsed: ParsedMessage;
  mentionNames?: Record<string, string>;
  message: Message;
  onVote?: (message: Message, options: string[]) => void;
}) {
  switch (parsed.kind) {
    case "deleted":
      return <p className="text-muted-foreground italic">This message was deleted</p>;

    case "text":
      return parsed.text ? (
        <p className="break-words whitespace-pre-wrap">
          {renderMarkdownText(parsed.text, mentionNames)}
        </p>
      ) : (
        <p className="text-muted-foreground italic">(empty message)</p>
      );

    case "media":
      return (
        <div className="space-y-1.5 pt-1">
          <MediaBody message={message} mediaType={parsed.mediaType} caption={parsed.caption} />
          {parsed.caption ? (
            <p className="break-words whitespace-pre-wrap">
              {renderMarkdownText(parsed.caption, mentionNames)}
            </p>
          ) : null}
        </div>
      );

    case "poll":
      return <PollBody parsed={parsed} message={message} onVote={onVote} />;

    case "location":
      return (
        <div className="space-y-1.5 pt-1">
          <div className="flex items-center gap-1.5 font-medium">
            <MapPin className="size-4 shrink-0 text-emerald-700 dark:text-emerald-400" aria-hidden />
            <span>{parsed.name || "Location"}</span>
          </div>
          {typeof parsed.latitude === "number" &&
          typeof parsed.longitude === "number" ? (
            <a
              href={`https://www.openstreetmap.org/?mlat=${parsed.latitude}&mlon=${parsed.longitude}#map=16/${parsed.latitude}/${parsed.longitude}`}
              target="_blank"
              rel="noopener noreferrer"
              className="flex min-w-56 items-center justify-between gap-3 rounded-lg bg-foreground/[0.04] px-2.5 py-2 text-xs transition-colors hover:bg-foreground/[0.07]"
            >
              <span className="font-mono tabular-nums">
                {parsed.latitude.toFixed(5)}, {parsed.longitude.toFixed(5)}
              </span>
              <span className="inline-flex items-center gap-1 font-medium">
                Open map
                <ExternalLink className="size-3" aria-hidden />
              </span>
            </a>
          ) : (
            <p className="text-xs text-muted-foreground">Coordinates unavailable.</p>
          )}
        </div>
      );

    case "contact":
      return (
        <div className="flex min-w-56 items-center gap-2.5 rounded-lg bg-foreground/[0.04] p-2 mt-1">
          <span className="flex size-9 shrink-0 items-center justify-center rounded-full bg-background ring-1 ring-border/60">
            <ContactIcon className="size-4 text-muted-foreground" aria-hidden />
          </span>
          <div className="min-w-0 flex-1 space-y-0.5">
            <p className="truncate font-medium">{parsed.name || "Shared contact"}</p>
            {parsed.phone ? (
              <p className="inline-flex items-center gap-1 text-xs text-muted-foreground">
                <Phone className="size-3" aria-hidden />
                {parsed.phone}
              </p>
            ) : null}
          </div>
        </div>
      );

    case "system":
      // Handled by the early return in MessageBubble; never reaches the bubble
      // body. Render nothing as a safety net.
      return null;

    case "unknown":
    default:
      return (
        <div className="space-y-0.5">
          <p className="text-muted-foreground italic">
            Unsupported message{parsed.type ? ` (${parsed.type})` : ""}.
          </p>
          {parsed.text ? (
            <p className="break-words whitespace-pre-wrap">{parsed.text}</p>
          ) : null}
        </div>
      );
  }
}

type MediaItem = NonNullable<Message["media"]>;

/** Inline media: images, video and audio play in place when the API returned a
 * stored URL; other files render as a download card. Without a stored copy the
 * card explains why and links to the session overview (storage settings). */
function MediaBody({
  message,
  mediaType,
  caption,
}: {
  message: Message;
  mediaType: string;
  caption?: string;
}) {
  const media = message.media;
  const items: Array<MediaItem | undefined> = media?.items?.length
    ? media.items
    : [media];
  return (
    <div className="flex flex-col gap-1.5">
      {items.map((item, index) => (
        <MediaItemView
          key={item?.id ?? index}
          item={item}
          index={items.length > 1 ? index + 1 : undefined}
          mediaType={mediaType}
          caption={caption}
          sessionId={message.sessionId}
        />
      ))}
    </div>
  );
}

function MediaItemView({
  item,
  index,
  mediaType,
  caption,
  sessionId,
}: {
  item: MediaItem | undefined;
  index?: number;
  mediaType: string;
  caption?: string;
  sessionId: string;
}) {
  const now = Date.now();
  const expired = Boolean(item?.expiresAt && item.expiresAt <= now);
  const url = item?.url && !expired ? item.url : undefined;
  const mime = item?.mimetype ?? "";
  const kind =
    mediaType === "image" || mediaType === "sticker" || mime.startsWith("image/")
      ? "image"
      : mediaType === "video" || mime.startsWith("video/")
        ? "video"
        : mediaType === "audio" ||
            mediaType === "ptt" ||
            mediaType === "voice" ||
            mime.startsWith("audio/")
          ? "audio"
          : "file";
  const label = item?.filename || `${capitalize(mediaType)}${index ? ` ${index}` : ""}`;

  if (url && kind === "image") {
    return (
      <a href={url} target="_blank" rel="noreferrer" className="block overflow-hidden rounded-lg">
        <img
          src={url}
          alt={caption || item?.filename || "Image"}
          loading="lazy"
          className={cn(
            "block max-h-72 w-auto max-w-full object-cover",
            mediaType === "sticker" && "max-h-36 bg-transparent",
          )}
        />
      </a>
    );
  }
  if (url && kind === "video") {
    return (
      <video
        src={url}
        controls
        preload="metadata"
        className="block max-h-72 max-w-full rounded-lg bg-black"
      />
    );
  }
  if (url && kind === "audio") {
    return <audio src={url} controls preload="metadata" className="h-10 w-64 max-w-full" />;
  }

  return (
    <Attachment
      state={url ? "done" : "idle"}
      className="min-w-56 border-border/70 bg-background/60 text-inherit dark:bg-background/20"
    >
      <AttachmentMedia variant="icon" className="bg-foreground/[0.05]">
        {url ? <FileText aria-hidden /> : <FileWarning aria-hidden className="text-muted-foreground" />}
      </AttachmentMedia>
      <AttachmentContent>
        <AttachmentTitle>{label}</AttachmentTitle>
        <AttachmentDescription>
          {url ? (
            <a href={url} target="_blank" rel="noreferrer" className="font-medium text-foreground underline-offset-2 hover:underline">
              Download{item?.size ? ` · ${formatBytes(item.size)}` : ""}
            </a>
          ) : expired ? (
            "No longer stored (retention ended)"
          ) : (
            <>
              Not stored.{" "}
              <Link
                to="/user/sessions/$sessionId"
                params={{ sessionId }}
                className="font-medium text-foreground underline-offset-2 hover:underline"
              >
                Set up storage
              </Link>
            </>
          )}
        </AttachmentDescription>
      </AttachmentContent>
    </Attachment>
  );
}

function capitalize(value: string): string {
  return value ? value[0]!.toUpperCase() + value.slice(1) : value;
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(0)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function PollBody({
  parsed,
  message,
  onVote,
}: {
  parsed: Extract<ParsedMessage, { kind: "poll" }>;
  message: Message;
  onVote?: (message: Message, options: string[]) => void;
}) {
  const selectableCount = parsed.selectableCount ?? 1;
  const maxSelections =
    selectableCount <= 0 ? parsed.options.length : Math.min(selectableCount, parsed.options.length);
  const [selected, setSelected] = useState<string[]>([]);
  const canVote = Boolean(onVote && parsed.options.length > 0);
  return (
    <div className="min-w-60 space-y-2 pt-1">
      <div className="space-y-0.5">
        <div className="flex items-center gap-1.5 font-medium">
          <ListChecks className="size-4 shrink-0 text-emerald-700 dark:text-emerald-400" aria-hidden />
          <span>{parsed.name || "Poll"}</span>
        </div>
        <p className="text-xs text-muted-foreground">
          {maxSelections > 1
            ? `Choose up to ${maxSelections} options`
            : "Choose one option"}
          {parsed.hideVotes ? " · votes hidden" : ""}
          {parsed.endTime ? ` · ends ${new Date(parsed.endTime).toLocaleString()}` : ""}
        </p>
      </div>
      {parsed.options.length > 0 ? (
        <div className="space-y-1.5" role="group" aria-label="Poll options">
          {parsed.options.map((opt, i) => {
            const isSelected = selected.includes(opt);
            return (
              <button
                type="button"
                key={`${i}-${opt}`}
                disabled={!canVote}
                aria-pressed={isSelected}
                onClick={() =>
                  setSelected((cur) => togglePollSelection(cur, opt, maxSelections))
                }
                className={cn(
                  "flex w-full items-center gap-2 rounded-lg border px-2.5 py-1.5 text-left text-xs transition-colors duration-150 ease-soft",
                  isSelected
                    ? "border-emerald-600/40 bg-emerald-500/10"
                    : "border-border/70 bg-background/60 dark:bg-background/20",
                  canVote && !isSelected && "hover:bg-foreground/[0.04]",
                )}
              >
                <span
                  className={cn(
                    "size-3.5 shrink-0 border",
                    maxSelections > 1 ? "rounded-[4px]" : "rounded-full",
                    isSelected
                      ? "border-emerald-600 bg-emerald-600 shadow-[inset_0_0_0_2px_var(--background)]"
                      : "border-muted-foreground/40",
                  )}
                  aria-hidden
                />
                <span className="min-w-0 flex-1">{opt}</span>
              </button>
            );
          })}
        </div>
      ) : (
        <p className="text-xs text-muted-foreground">No options available.</p>
      )}
      {canVote ? (
        <Button
          type="button"
          size="sm"
          variant="outline"
          className="h-7 w-full text-xs"
          disabled={selected.length === 0}
          onClick={() => onVote?.(message, selected)}
        >
          Vote
        </Button>
      ) : null}
    </div>
  );
}

function togglePollSelection(cur: string[], option: string, max: number): string[] {
  if (cur.includes(option)) return cur.filter((x) => x !== option);
  if (max <= 1) return [option];
  if (cur.length >= max) return [...cur.slice(1), option];
  return [...cur, option];
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
