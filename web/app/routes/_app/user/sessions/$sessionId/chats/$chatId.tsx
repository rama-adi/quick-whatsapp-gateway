// Viewer: one chat's message timeline + composer. Surface: viewer. Child of the
// chats list — renders in its <Outlet/>.
//
//   - SSR: the loader seeds the chat header (qk.chat) + page 0 of the message
//     timeline (qk.chatMessages) via Drizzle direct reads (-viewer-data.ts).
//   - Realtime: live inbound/outbound messages are prepended to page 0 of the
//     cache by the shared cacheBridge ("message"/"message.from_me"); statuses
//     patched by "message.status"; reactions/edits patch the body. We read the
//     cache + re-render.
//   - Display: flatten pages, sort ascending (oldest→newest), group by day and
//     into same-sender runs. Newest sits at the bottom; new arrivals auto-scroll
//     there, and "Load older" prepends without yanking the viewport.
//   - Components: the timeline is the styled shadcn MessageScroller set; each
//     message is a Bubble inside a Message; day dividers are a Marker; the
//     media "not downloaded" placeholder is an Attachment (see -message-bubble).
//   - Composer: send goes through useSendMessage (optimistic), with text plus
//     structured WhatsApp payload helpers.

import { useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { useQuery, type InfiniteData } from "@tanstack/react-query";
import {
  ChevronDown,
  Contact,
  Image,
  ListChecks,
  MapPin,
  MessageSquare,
  Paperclip,
  SendHorizonal,
} from "lucide-react";
import {
  MessageScroller,
  MessageScrollerProvider,
  MessageScrollerViewport,
  MessageScrollerContent,
  MessageScrollerItem,
  MessageScrollerButton,
  useMessageScrollerScrollable,
} from "~/components/ui/message-scroller";
import {
  useChatMessages,
  useSendMessage,
  useVoteMessage,
} from "~/lib/api/hooks/messages";
import { useChat, useMarkChatRead } from "~/lib/api/hooks/chats";
import { qk } from "~/lib/query";
import type { Chat, Message } from "~/lib/api/types";
import type { Page } from "~/lib/api/envelope";
import { isApiError } from "~/lib/api/envelope";
import { usePollingInterval } from "~/lib/events/useEventStream";
import { Button } from "~/components/ui/button";
import { Textarea } from "~/components/ui/textarea";
import { Input } from "~/components/ui/input";
import { Label } from "~/components/ui/label";
import { Spinner } from "~/components/ui/spinner";
import { TooltipProvider } from "~/components/ui/tooltip";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "~/components/ui/dropdown-menu";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "~/components/ui/dialog";
import { Marker, MarkerContent } from "~/components/ui/marker";
import { BubbleGroup } from "~/components/ui/bubble";
import { Badge } from "~/components/ui/badge";
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "~/components/ui/empty";
import { toast } from "sonner";
import { ChatAvatar, formatDayHeading, dayKey } from "./-viewer-ui";
import { MessageBubble } from "./-message-bubble";
import { fetchChat, fetchMessagesPage } from "./-viewer-data";

type ViewerChat = Chat & {
  participantCount?: number;
  isAnnounce?: boolean;
  isLocked?: boolean;
};

type ComposerSend =
  | { type: "text"; text: string }
  | {
      type: "poll";
      name: string;
      options: string[];
      selectableCount: number;
      pollEndTime?: number;
      pollHideVotes?: boolean;
    }
  | { type: "location"; name?: string; latitude: number; longitude: number }
  | { type: "contact"; contact: { name: string; phone?: string; jid?: string } }
  | {
      type: "image" | "video" | "audio" | "document" | "sticker";
      media: { data: string; mimetype: string; filename?: string; caption?: string };
    };

export const Route = createFileRoute(
  "/_app/user/sessions/$sessionId/chats/$chatId",
)({
  loader: async ({ params, context }) => {
    const { sessionId, chatId } = params;
    try {
      await Promise.all([
        context.queryClient.ensureQueryData({
          queryKey: qk.chat(sessionId, chatId),
          queryFn: async (): Promise<Chat> => {
            const chat = await fetchChat({
              data: { sessionId, chatJid: chatId },
            });
            // ensureQueryData must resolve a value; a minimal stub lets the client
            // hook refetch + the header render the jid until the gateway answers.
            // The non-jid fields are placeholders, overwritten by the real fetch.
            return (
              chat ?? {
                id: 0,
                sessionId,
                jid: chatId,
                type: "dm",
                unreadCount: 0,
                archived: false,
                pinned: false,
              }
            );
          },
        }),
        context.queryClient.ensureInfiniteQueryData({
          queryKey: qk.chatMessages(sessionId, chatId),
          initialPageParam: undefined as string | undefined,
          queryFn: () =>
            fetchMessagesPage({ data: { sessionId, chatJid: chatId } }),
          getNextPageParam: (last: unknown) =>
            (last as Page<Message>).nextCursor ?? undefined,
        }),
      ]);
    } catch (err) {
      console.warn("[viewer] failed to preload chat timeline:", err);
    }
  },
  component: ViewerTimeline,
});

/** A run of consecutive messages from the same sender within one day. */
type MessageRun = {
  /** Stable id for the scroller item + react key (last message's id). */
  id: string;
  /** Day-divider label, set on the first run of each calendar day. */
  dayHeading: string | null;
  messages: Message[];
};

/** Identity used to break same-sender runs. In groups the sender is carried on
 * senderLid (senderJid is absent), so prefer it; fall back to senderJid. */
function senderKey(m: Message): string {
  return m.senderLid ?? m.senderJid ?? "";
}

function messageId(m: Message): string {
  return m.waMessageId || m.id || `${m.timestamp}-${senderKey(m)}`;
}

/** Group an ascending message list into same-sender runs, tagging day breaks. */
function groupRuns(ordered: Message[]): MessageRun[] {
  const runs: MessageRun[] = [];
  let lastDay = "";
  let prevKey: string | null = null;

  for (const m of ordered) {
    const dk = dayKey(m.timestamp);
    const dayBreak = dk !== lastDay;
    lastDay = dk;
    // Group key: same direction + sender, broken by a day boundary.
    const runKey = `${dk}|${m.direction}|${senderKey(m)}`;
    const last = runs[runs.length - 1];

    if (!dayBreak && last && runKey === prevKey) {
      last.messages.push(m);
      last.id = messageId(m);
    } else {
      runs.push({
        id: messageId(m),
        dayHeading: dayBreak ? formatDayHeading(m.timestamp) : null,
        messages: [m],
      });
    }
    prevKey = runKey;
  }
  return runs;
}

function ViewerTimeline() {
  const { sessionId, chatId } = Route.useParams();
  const chat = useChat(sessionId, chatId);
  const messages = useChatMessages(sessionId, chatId);
  const markRead = useMarkChatRead(sessionId);
  const send = useSendMessage(sessionId);
  const vote = useVoteMessage(sessionId);
  const pollMs = usePollingInterval();
  const presence = useQuery<Record<string, unknown>>({
    queryKey: qk.presence(sessionId, chatId),
    enabled: false,
    staleTime: Infinity,
    queryFn: async () => ({}),
  });
  const lastReadKey = useRef<string | null>(null);
  const loadingOlder = useRef(false);
  const [replyTo, setReplyTo] = useState<Message | null>(null);
  const [activeTypingText, setActiveTypingText] = useState<string | null>(null);

  // Polling fallback when the live stream is degraded.
  useEffect(() => {
    if (!pollMs) return;
    const id = window.setInterval(() => {
      void messages.refetch();
    }, pollMs);
    return () => window.clearInterval(id);
  }, [pollMs, messages]);

  useEffect(() => {
    loadingOlder.current = messages.isFetchingNextPage;
  }, [messages.isFetchingNextPage]);

  // Flatten all pages (newest-first) → chronological (oldest-first) for display.
  const ordered = useMemo<Message[]>(() => {
    const flat =
      (messages.data as InfiniteData<Page<Message>> | undefined)?.pages.flatMap(
        (p) => p.data,
      ) ?? [];
    // Dedupe by id before sorting. A refetch-on-mount (e.g. switching chats)
    // re-pages with cursors; if a live message was prepended to page 0 while we
    // were away, the page boundary shifts and the same message can come back in
    // two adjacent pages. Keep the last occurrence (most recently written).
    const byId = new Map<string, Message>();
    for (const m of flat) byId.set(messageId(m), m);
    return [...byId.values()].sort(
      (a, b) => (a.timestamp ?? 0) - (b.timestamp ?? 0),
    );
  }, [messages.data]);

  const runs = useMemo(() => groupRuns(ordered), [ordered]);
  const messagesById = useMemo(() => {
    const byId = new Map<string, Message>();
    for (const message of ordered) {
      byId.set(messageId(message), message);
      if (message.waMessageId) byId.set(message.waMessageId, message);
      if (message.id) byId.set(message.id, message);
    }
    return byId;
  }, [ordered]);

  // "Load older" feeds the existing infinite-query. MessageScroller's
  // preserveScrollOnPrepend keeps the reading position fixed when the prepended
  // page lands, so no manual scroll-offset bookkeeping is needed here.
  const canLoadOlder = Boolean(messages.hasNextPage);
  const isFetchingOlder = messages.isFetchingNextPage;
  const loadOlder = () => {
    if (canLoadOlder && !loadingOlder.current) {
      loadingOlder.current = true;
      void messages.fetchNextPage().finally(() => {
        loadingOlder.current = false;
      });
    }
  };

  const viewerChat = chat.data as ViewerChat | undefined;
  const title = viewerChat?.name || viewerChat?.jid || chatId;
  const unread = chat.data?.unreadCount ?? 0;
  const presenceState =
    typeof presence.data?.state === "string" ? presence.data.state : undefined;
  const presenceMedia =
    typeof presence.data?.media === "string" ? presence.data.media : undefined;
  const typingText =
    presenceState === "composing"
      ? "typing..."
      : presenceState === "recording" || presenceMedia === "audio"
        ? "recording audio..."
        : null;
  const headerMeta = chatHeaderMeta(viewerChat, chatId, presenceState);

  useEffect(() => {
    if (!typingText) {
      setActiveTypingText(null);
      return;
    }
    setActiveTypingText(typingText);
    const id = window.setTimeout(() => setActiveTypingText(null), 4_500);
    return () => window.clearTimeout(id);
  }, [chatId, typingText]);

  useEffect(() => {
    if (unread <= 0 || markRead.isPending) return;
    const key = `${chatId}:${unread}`;
    if (lastReadKey.current === key) return;
    lastReadKey.current = key;
    markRead.mutate(
      { chatId },
      {
        onError: () => {
          lastReadKey.current = null;
        },
      },
    );
  }, [chatId, markRead, unread]);

  return (
    <TooltipProvider>
      <div className="h-full min-h-[60svh] md:min-h-0">
        <section
          aria-label="Conversation"
          className="flex min-h-0 flex-col rounded-lg border bg-card"
        >
          <header className="flex items-center gap-3 border-b px-4 py-2">
            <div className="relative">
              <ChatAvatar
                sessionId={sessionId}
                name={viewerChat?.name}
                jid={viewerChat?.jid}
                type={viewerChat?.type}
              />
              {viewerChat?.type === "dm" ? (
                <span
                  className="absolute bottom-0 right-0 size-2.5 rounded-full border-2 border-card bg-emerald-500"
                  aria-label={presenceState === "available" ? "Online" : "Presence unknown"}
                />
              ) : null}
            </div>
            <div className="min-w-0 flex-1">
              <h2 className="truncate text-sm font-semibold">{title}</h2>
              <p className="truncate text-xs text-muted-foreground">
                {headerMeta}
              </p>
            </div>
            {viewerChat?.type === "group" || viewerChat?.type === "newsletter" ? (
              <Badge variant="secondary" className="hidden shrink-0 sm:inline-flex">
                {viewerChat.type === "newsletter" ? "Channel" : "Group"}
              </Badge>
            ) : null}
          </header>

          {/* Styled shadcn MessageScroller. Provider tracks scroll state;
            autoScroll follows the bottom on new arrivals only while the user is
            already at the end; defaultScrollPosition="end" opens pinned to the
            newest message. The Viewport is the scrollable element
            (preserveScrollOnPrepend keeps the position fixed when "Load older"
            prepends a page). */}
          <MessageScrollerProvider autoScroll defaultScrollPosition="end">
            <MessageScroller className="min-h-0 flex-1">
              <MessageScrollerViewport
                preserveScrollOnPrepend
                role="log"
                aria-label="Message timeline"
                aria-live="polite"
                tabIndex={0}
                onScroll={(e) => {
                  // Near the top edge → pull the next (older) page. The library
                  // restores the offset afterward via preserveScrollOnPrepend.
                  if (e.currentTarget.scrollTop <= 32) loadOlder();
                }}
                className="outline-none focus-visible:ring-1 focus-visible:ring-ring"
              >
                <MessageScrollerContent className="gap-2 p-4">
                  <TimelineBody
                    runs={runs}
                    isLoading={messages.isLoading}
                    isError={messages.isError}
                    error={messages.error}
                    hasOlder={canLoadOlder}
                    isFetchingOlder={isFetchingOlder}
                    onLoadOlder={loadOlder}
                    onReply={setReplyTo}
                    onVote={(message, options) =>
                      vote.mutate(
                        { messageId: messageId(message), chatId, options },
                        {
                          onError: (e) =>
                            toast.error(
                              isApiError(e) ? e.message : "Failed to vote",
                            ),
                        },
                      )
                    }
                    messagesById={messagesById}
                  />
                </MessageScrollerContent>
              </MessageScrollerViewport>

              <JumpToLatest />
            </MessageScroller>
          </MessageScrollerProvider>

          <Composer
            disabled={send.isPending}
            chatType={viewerChat?.type}
            chat={viewerChat}
            typingText={activeTypingText}
            replyTo={replyTo}
            onCancelReply={() => setReplyTo(null)}
            onSend={(payload) =>
              send.mutate(
                {
                  to: chatId,
                  ...payload,
                  ...(replyTo ? { replyTo: messageId(replyTo) } : {}),
                },
                {
                  onSuccess: () => setReplyTo(null),
                  onError: (e) =>
                    toast.error(isApiError(e) ? e.message : "Failed to send"),
                },
              )
            }
          />
        </section>
      </div>
    </TooltipProvider>
  );
}

function chatHeaderMeta(
  chat: ViewerChat | undefined,
  chatId: string,
  presenceState?: string,
): string {
  if (chat?.type === "dm") {
    if (presenceState === "available") return "Online";
    if (presenceState === "unavailable") return "Offline";
    return chat.jid ?? chatId;
  }
  if (chat?.type === "group") {
    const count = chat.participantCount
      ? `${chat.participantCount.toLocaleString()} members`
      : "Group chat";
    if (chat.isAnnounce) return `${count} · announcements only`;
    if (chat.isLocked) return `${count} · admins manage settings`;
    return count;
  }
  if (chat?.type === "newsletter") return "Announcement channel";
  if (chat?.type === "broadcast") return "Broadcast list";
  if (chat?.type === "status") return "Status updates";
  return chatId;
}

function TimelineBody({
  runs,
  isLoading,
  isError,
  error,
  hasOlder,
  isFetchingOlder,
  onLoadOlder,
  onReply,
  onVote,
  messagesById,
}: {
  runs: MessageRun[];
  isLoading: boolean;
  isError: boolean;
  error: unknown;
  hasOlder: boolean;
  isFetchingOlder: boolean;
  onLoadOlder: () => void;
  onReply: (message: Message) => void;
  onVote: (message: Message, options: string[]) => void;
  messagesById: Map<string, Message>;
}) {
  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-12 text-sm text-muted-foreground">
        <Spinner className="mr-2" />
        Loading messages…
      </div>
    );
  }

  if (isError) {
    return (
      <Empty className="border-0">
        <EmptyHeader>
          <EmptyTitle>Couldn’t load messages</EmptyTitle>
          <EmptyDescription>
            {isApiError(error) ? error.message : "Something went wrong."}
          </EmptyDescription>
        </EmptyHeader>
      </Empty>
    );
  }

  if (runs.length === 0) {
    return (
      <Empty className="border-0">
        <EmptyHeader>
          <EmptyMedia variant="icon">
            <MessageSquare />
          </EmptyMedia>
          <EmptyTitle>No messages yet</EmptyTitle>
          <EmptyDescription>
            Send the first message to start this conversation.
          </EmptyDescription>
        </EmptyHeader>
      </Empty>
    );
  }

  const lastIndex = runs.length - 1;

  return (
    <>
      {hasOlder ? (
        <div className="flex justify-center pb-1">
          <TopLoadSentinel
            disabled={isFetchingOlder}
            onVisible={onLoadOlder}
          />
          <Button
            size="sm"
            variant="ghost"
            disabled={isFetchingOlder}
            onClick={onLoadOlder}
          >
            {isFetchingOlder ? "Loading…" : "Load older messages"}
          </Button>
        </div>
      ) : null}

      {runs.map((run, i) => (
        // One scroller Item per same-sender run. The scroller anchors and tracks
        // visibility by messageId; the newest run is the scrollAnchor (where
        // "end" positioning and autoScroll-follow land).
        <MessageScrollerItem
          key={run.id}
          messageId={run.id}
          scrollAnchor={i === lastIndex}
        >
          {run.dayHeading ? (
            <Marker variant="separator" className="my-1">
              <MarkerContent>{run.dayHeading}</MarkerContent>
            </Marker>
          ) : null}
          <BubbleGroup>
            {run.messages.map((m, j) => (
              <MessageBubble
                key={messageId(m)}
                message={m}
                quotedMessage={
                  m.quotedMessageId ? messagesById.get(m.quotedMessageId) : undefined
                }
                showSender={j === 0}
                onReply={onReply}
                onVote={onVote}
              />
            ))}
          </BubbleGroup>
        </MessageScrollerItem>
      ))}
    </>
  );
}

function TopLoadSentinel({
  disabled,
  onVisible,
}: {
  disabled: boolean;
  onVisible: () => void;
}) {
  const ref = useRef<HTMLSpanElement | null>(null);
  useEffect(() => {
    const node = ref.current;
    if (!node || disabled) return;
    const observer = new IntersectionObserver(
      ([entry]) => {
        if (entry?.isIntersecting) onVisible();
      },
      { root: node.closest("[data-slot='message-scroller-viewport']") },
    );
    observer.observe(node);
    return () => observer.disconnect();
  }, [disabled, onVisible]);
  return <span ref={ref} className="size-px" aria-hidden />;
}

// Floating "jump to latest" affordance. MessageScrollerButton(direction="end")
// is self-hiding: the library marks it inert + tabIndex=-1 and exposes an
// `active` render-state flag that mirrors useMessageScrollerScrollable().end —
// true only when there is unseen content below (the user has scrolled up). We
// hide it entirely when inactive so it doesn't yank focus or clutter the view.
function JumpToLatest() {
  const { end } = useMessageScrollerScrollable();
  if (!end) return null;
  return (
    <MessageScrollerButton
      direction="end"
      behavior="smooth"
      aria-label="Jump to latest messages"
      variant="secondary"
      size="sm"
      className="left-1/2 rounded-full px-3 text-xs font-medium shadow-md"
    >
      <span className="flex items-center gap-1">
        <ChevronDown className="size-3.5" aria-hidden />
        Jump to latest
      </span>
    </MessageScrollerButton>
  );
}

function Composer({
  disabled,
  chatType,
  chat,
  typingText,
  replyTo,
  onCancelReply,
  onSend,
}: {
  disabled: boolean;
  chatType?: Chat["type"];
  chat?: ViewerChat;
  typingText: string | null;
  replyTo: Message | null;
  onCancelReply: () => void;
  onSend: (payload: ComposerSend) => void;
}) {
  const [text, setText] = useState("");
  const [dialog, setDialog] = useState<"poll" | "location" | "contact" | "media" | null>(
    null,
  );

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const trimmed = text.trim();
    if (!trimmed) return;
    onSend({ type: "text", text: trimmed });
    setText("");
  };

  return (
    <>
      <form
        onSubmit={submit}
        className="space-y-2 border-t px-3 pb-3 pt-2"
        aria-label="Send a message"
      >
        <TypingIndicator chat={chat} text={typingText} />
        {replyTo ? (
          <div className="flex items-start justify-between gap-3 rounded-md border bg-muted/40 px-3 py-2 text-xs">
            <div className="min-w-0">
              <p className="font-medium">Replying to {replyAuthor(replyTo)}</p>
              <p className="truncate text-muted-foreground">{replyPreview(replyTo)}</p>
            </div>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="h-7 px-2"
              onClick={onCancelReply}
            >
              Cancel
            </Button>
          </div>
        ) : null}
        <div className="flex items-end gap-2">
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button
                type="button"
                size="icon"
                variant="outline"
                disabled={disabled}
                aria-label="Attach or send rich content"
              >
                <Paperclip className="size-4" aria-hidden />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="start" side="top" className="w-56">
              <DropdownMenuItem onClick={() => setDialog("poll")}>
                <ListChecks /> Poll
              </DropdownMenuItem>
              <DropdownMenuItem onClick={() => setDialog("location")}>
                <MapPin /> Location
              </DropdownMenuItem>
              <DropdownMenuItem onClick={() => setDialog("contact")}>
                <Contact /> Contact
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem onClick={() => setDialog("media")}>
                <Image /> Media placeholder
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
          <Textarea
            value={text}
            onChange={(e) => setText(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !e.shiftKey) {
                e.preventDefault();
                (e.currentTarget.form as HTMLFormElement | null)?.requestSubmit();
              }
            }}
            placeholder={
              chatType === "newsletter"
                ? "Write an announcement"
                : "Type a message with markdown"
            }
            aria-label="Message"
            rows={1}
            className="max-h-32 min-h-9 flex-1 resize-none"
          />
          <Button
            type="submit"
            size="icon"
            disabled={disabled || text.trim().length === 0}
            aria-label="Send"
          >
            {disabled ? <Spinner /> : <SendHorizonal className="size-4" />}
          </Button>
        </div>
      </form>
      <RichSendDialog
        kind={dialog}
        onOpenChange={(open) => {
          if (!open) setDialog(null);
        }}
        onSend={(payload) => {
          onSend(payload);
          setDialog(null);
        }}
      />
    </>
  );
}

function RichSendDialog({
  kind,
  onOpenChange,
  onSend,
}: {
  kind: "poll" | "location" | "contact" | "media" | null;
  onOpenChange: (open: boolean) => void;
  onSend: (payload: ComposerSend) => void;
}) {
  const [question, setQuestion] = useState("");
  const [options, setOptions] = useState("Yes\nNo\nMaybe");
  const [selectableCount, setSelectableCount] = useState("1");
  const [pollEndTime, setPollEndTime] = useState("");
  const [pollHideVotes, setPollHideVotes] = useState(false);
  const [place, setPlace] = useState("");
  const [lat, setLat] = useState("");
  const [lng, setLng] = useState("");
  const [name, setName] = useState("");
  const [phone, setPhone] = useState("");
  const [caption, setCaption] = useState("");
  const open = kind !== null;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{richDialogTitle(kind)}</DialogTitle>
          <DialogDescription>
            Compose a structured WhatsApp payload for this chat.
          </DialogDescription>
        </DialogHeader>
        {kind === "poll" ? (
          <div className="space-y-3">
            <Field label="Question" value={question} onChange={setQuestion} />
            <div className="space-y-1.5">
              <Label>Options</Label>
              <Textarea
                value={options}
                onChange={(e) => setOptions(e.target.value)}
                rows={4}
              />
            </div>
            <div className="grid grid-cols-2 gap-2">
              <Field
                label="Selectable"
                value={selectableCount}
                onChange={setSelectableCount}
                type="number"
              />
              <Field
                label="Ends at"
                value={pollEndTime}
                onChange={setPollEndTime}
                type="datetime-local"
              />
            </div>
            <label className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={pollHideVotes}
                onChange={(e) => setPollHideVotes(e.currentTarget.checked)}
                className="size-4 accent-primary"
              />
              Hide voter names
            </label>
            <Button
              onClick={() => {
                const rows = options
                  .split("\n")
                  .map((x) => x.trim())
                  .filter(Boolean);
                if (!question.trim() || rows.length < 2) return;
                const selectable = Math.max(
                  1,
                  Math.min(Number(selectableCount) || 1, rows.length),
                );
                const endTime = pollEndTime ? new Date(pollEndTime).getTime() : 0;
                onSend({
                  type: "poll",
                  name: question.trim(),
                  options: rows,
                  selectableCount: selectable,
                  ...(Number.isFinite(endTime) && endTime > 0
                    ? { pollEndTime: endTime }
                    : {}),
                  ...(pollHideVotes ? { pollHideVotes: true } : {}),
                });
              }}
            >
              Send poll
            </Button>
          </div>
        ) : null}
        {kind === "location" ? (
          <div className="space-y-3">
            <Field label="Place label" value={place} onChange={setPlace} />
            <div className="grid grid-cols-2 gap-2">
              <Field label="Latitude" value={lat} onChange={setLat} />
              <Field label="Longitude" value={lng} onChange={setLng} />
            </div>
            <Button
              onClick={() => {
                const latitude = Number(lat);
                const longitude = Number(lng);
                if (!Number.isFinite(latitude) || !Number.isFinite(longitude)) return;
                onSend({
                  type: "location",
                  name: place.trim() || undefined,
                  latitude,
                  longitude,
                });
              }}
            >
              Send location
            </Button>
          </div>
        ) : null}
        {kind === "contact" ? (
          <div className="space-y-3">
            <Field label="Name" value={name} onChange={setName} />
            <Field label="Phone" value={phone} onChange={setPhone} />
            <Button
              onClick={() => {
                if (!name.trim()) return;
                onSend({
                  type: "contact",
                  contact: { name: name.trim(), phone: phone.trim() || undefined },
                });
              }}
            >
              Send contact
            </Button>
          </div>
        ) : null}
        {kind === "media" ? (
          <div className="space-y-3">
            <Field label="Caption" value={caption} onChange={setCaption} />
            <Button
              onClick={() =>
                onSend({
                  type: "document",
                  media: {
                    data: "",
                    mimetype: "application/octet-stream",
                    filename: "placeholder.txt",
                    caption: caption.trim() || undefined,
                  },
                })
              }
            >
              Queue placeholder
            </Button>
          </div>
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

function Field({
  label,
  value,
  onChange,
  type = "text",
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  type?: string;
}) {
  return (
    <div className="space-y-1.5">
      <Label>{label}</Label>
      <Input type={type} value={value} onChange={(e) => onChange(e.target.value)} />
    </div>
  );
}

function richDialogTitle(kind: "poll" | "location" | "contact" | "media" | null) {
  switch (kind) {
    case "poll":
      return "Send poll";
    case "location":
      return "Send location";
    case "contact":
      return "Send contact";
    case "media":
      return "Attach media";
    default:
      return "Send rich content";
  }
}

function TypingIndicator({ chat, text }: { chat?: ViewerChat; text: string | null }) {
  return (
    <div
      className="flex h-5 items-center gap-2 px-1 text-[11px] text-muted-foreground"
      aria-live="polite"
    >
      {text ? (
        <>
          <div className="flex -space-x-1.5">
            <ChatAvatar
              sessionId={chat?.sessionId}
              name={chat?.name}
              jid={chat?.jid}
              type={chat?.type}
              className="size-5 border border-card text-[9px]"
            />
          </div>
          <span className="relative overflow-hidden whitespace-nowrap">
            <span className="animate-pulse">{text}</span>
          </span>
        </>
      ) : null}
    </div>
  );
}

function replyAuthor(message: Message): string {
  if (message.direction === "out") return "you";
  return message.senderName || message.senderLid || message.senderJid || "sender";
}

function replyPreview(message: Message): string {
  const body = message.body?.trim();
  if (body) return body.length > 140 ? `${body.slice(0, 140)}...` : body;
  return message.type ? `${message.type} message` : "Message";
}
