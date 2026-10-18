// Viewer: one chat's message timeline + composer. Surface: viewer. Child of the
// chats list — renders in its <Outlet/> and fills the rest of the inset panel.
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
//     message is a Bubble inside a Message; day dividers are a Marker pill.
//   - Composer (-composer.tsx): send goes through useSendMessage (optimistic);
//     a failed send puts the typed text back. Disabled while the session isn't
//     connected.
//   - Mobile: the chats layout shows only this pane; the header's back button
//     returns to the list.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createFileRoute, Link } from "@tanstack/react-router";
import { type InfiniteData } from "@tanstack/react-query";
import {
  ArrowLeft,
  ChevronDown,
  Copy,
  MessageSquare,
  MoreVertical,
  Phone,
  UserRound,
} from "lucide-react";
import { toast } from "sonner";
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
import {
  useChat,
  useChatPresence,
  useMarkChatRead,
} from "~/lib/api/hooks/chats";
import { useSession, useSessionLifecycle } from "~/lib/api/hooks/sessions";
import type { Message } from "~/lib/api/types";
import type { Page } from "~/lib/api/envelope";
import { isApiError } from "~/lib/api/envelope";
import { usePollingInterval } from "~/lib/events/useEventStream";
import { cn } from "~/lib/utils";
import { copyText } from "~/components/copyable";
import { Button } from "~/components/ui/button";
import { Spinner } from "~/components/ui/spinner";
import { TooltipProvider } from "~/components/ui/tooltip";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "~/components/ui/dropdown-menu";
import { Marker, MarkerContent } from "~/components/ui/marker";
import { BubbleGroup } from "~/components/ui/bubble";
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "~/components/ui/empty";
import { ChatAvatar, formatDayHeading, dayKey, prettyName } from "./-viewer-ui";
import { MessageBubble } from "./-message-bubble";
import { Composer, type ViewerChat } from "./-composer";

export const Route = createFileRoute(
  "/_app/user/sessions/$sessionId/chats/$chatId",
)({
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
  const session = useSession(sessionId);
  const lifecycle = useSessionLifecycle();
  const vote = useVoteMessage(sessionId);
  const pollMs = usePollingInterval();
  const isDirectChat = (chat.data as ViewerChat | undefined)?.type === "dm";
  const presence = useChatPresence(sessionId, chatId, isDirectChat);
  const lastReadKey = useRef<string | null>(null);
  const loadingOlder = useRef(false);
  const lastRequestedOlderCursor = useRef<string | null>(null);
  const wasNearTop = useRef(false);
  const viewportRef = useRef<HTMLDivElement | null>(null);
  const pinnedInitialChat = useRef<string | null>(null);
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

  useEffect(() => {
    pinnedInitialChat.current = null;
    wasNearTop.current = false;
    lastRequestedOlderCursor.current = null;
  }, [chatId]);

  useEffect(() => {
    if (ordered.length === 0 || pinnedInitialChat.current === chatId) return;
    const viewport = viewportRef.current;
    if (!viewport) return;
    pinnedInitialChat.current = chatId;
    requestAnimationFrame(() => {
      viewport.scrollTop = viewport.scrollHeight;
      wasNearTop.current = false;
    });
  }, [chatId, ordered.length]);

  // "Load older" feeds the existing infinite-query. MessageScroller's
  // preserveScrollOnPrepend keeps the reading position fixed when the prepended
  // page lands, so no manual scroll-offset bookkeeping is needed here.
  const canLoadOlder = Boolean(messages.hasNextPage);
  const isFetchingOlder = messages.isFetchingNextPage;
  const nextOlderCursor =
    (messages.data as InfiniteData<Page<Message>> | undefined)?.pages.at(-1)
      ?.nextCursor ?? null;

  useEffect(() => {
    lastRequestedOlderCursor.current = null;
  }, [nextOlderCursor]);

  const loadOlder = useCallback(() => {
    if (canLoadOlder && !loadingOlder.current && nextOlderCursor) {
      if (lastRequestedOlderCursor.current === nextOlderCursor) return;
      lastRequestedOlderCursor.current = nextOlderCursor;
      loadingOlder.current = true;
      void messages
        .fetchNextPage()
        .catch(() => {
          lastRequestedOlderCursor.current = null;
        })
        .finally(() => {
          loadingOlder.current = false;
        });
    }
  }, [canLoadOlder, messages, nextOlderCursor]);

  const viewerChat = chat.data as ViewerChat | undefined;
  const title = prettyName(viewerChat?.name) || chatPhone(viewerChat, chatId) || chatId;
  const isGroup = viewerChat?.type === "group";
  const unread = chat.data?.unreadCount ?? 0;
  const presenceState =
    typeof presence.data?.state === "string" ? presence.data.state : undefined;
  const presenceMedia =
    typeof presence.data?.media === "string" ? presence.data.media : undefined;
  const typingText =
    presenceState === "composing"
      ? presenceMedia === "audio"
        ? "recording audio..."
        : "typing..."
      : presenceMedia === "audio"
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
    <TooltipProvider delayDuration={400}>
      <section
        aria-label="Conversation"
        className="flex h-full min-h-0 flex-1 flex-col overflow-hidden bg-muted/70 dark:bg-background"
      >
        <header className="flex h-14 shrink-0 items-center gap-2 border-b border-border/60 bg-background/80 px-2 backdrop-blur-sm sm:gap-3 sm:px-4">
          <Button
            asChild
            variant="ghost"
            size="icon-sm"
            className="shrink-0 text-muted-foreground md:hidden"
          >
            <Link
              to="/user/sessions/$sessionId/chats"
              params={{ sessionId }}
              aria-label="Back to chats"
            >
              <ArrowLeft aria-hidden />
            </Link>
          </Button>
          <div className="relative shrink-0">
            <ChatAvatar
              sessionId={sessionId}
              name={viewerChat?.name}
              jid={viewerChat?.jid}
              type={viewerChat?.type}
            />
            {viewerChat?.type === "dm" && presenceState === "available" ? (
              <span
                className="absolute right-0 bottom-0 size-2.5 rounded-full bg-emerald-500 ring-2 ring-background"
                aria-label="Online"
              />
            ) : null}
          </div>
          <div className="min-w-0 flex-1">
            <h2 className="truncate text-sm font-semibold">{title}</h2>
            <p
              className={cn(
                "truncate text-xs",
                presenceState === "available" && viewerChat?.type === "dm"
                  ? "text-emerald-700 dark:text-emerald-400"
                  : "text-muted-foreground",
              )}
            >
              {headerMeta}
            </p>
          </div>
          <ChatHeaderMenu sessionId={sessionId} chat={viewerChat} chatId={chatId} />
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
              ref={viewportRef}
              preserveScrollOnPrepend
              role="log"
              aria-label="Message timeline"
              aria-live="polite"
              tabIndex={0}
              onScroll={(e) => {
                // Near the top edge → pull the next (older) page. The library
                // restores the offset afterward via preserveScrollOnPrepend.
                const top = e.currentTarget.scrollTop;
                if (top > 96) {
                  wasNearTop.current = false;
                  return;
                }
                if (top <= 32 && !wasNearTop.current) {
                  wasNearTop.current = true;
                  loadOlder();
                }
              }}
              className="outline-none [scrollbar-width:thin] focus-visible:ring-1 focus-visible:ring-ring/40 focus-visible:ring-inset"
            >
              <MessageScrollerContent className="mx-auto w-full max-w-4xl gap-3 px-3 py-4 sm:px-6">
                <TimelineBody
                  runs={runs}
                  isGroup={isGroup}
                  isLoading={messages.isLoading}
                  isError={messages.isError}
                  error={messages.error}
                  onRetry={() => void messages.refetch()}
                  hasOlder={canLoadOlder}
                  isFetchingOlder={isFetchingOlder}
                  onLoadOlder={loadOlder}
                  onReply={setReplyTo}
                  onVote={(message, options) =>
                    vote.mutate(
                      { messageId: messageId(message), chatId, options },
                      {
                        onSuccess: () => toast.success("Vote sent"),
                        onError: (e) =>
                          toast.error(
                            isApiError(e) ? e.message : "Couldn't send your vote",
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

        <div className="mx-auto w-full max-w-4xl">
          <Composer
            sessionId={sessionId}
            sessionStatus={session.data?.status}
            onStartSession={() =>
              lifecycle.mutate(
                { sessionId, action: "start" },
                {
                  onError: (e) =>
                    toast.error(isApiError(e) ? e.message : "Couldn't start the session"),
                },
              )
            }
            startingSession={lifecycle.isPending}
            sending={send.isPending}
            chat={viewerChat}
            typingText={activeTypingText}
            replyTo={replyTo}
            onCancelReply={() => setReplyTo(null)}
            onSend={(payload) =>
              send
                .mutateAsync({
                  to: chatId,
                  ...payload,
                  ...(replyTo ? { replyTo: messageId(replyTo) } : {}),
                })
                .then(
                  () => setReplyTo(null),
                  (e: unknown) => {
                    toast.error(
                      isApiError(e)
                        ? `Message not sent: ${e.message}`
                        : "Message not sent. Your text is back in the box.",
                    );
                    throw e;
                  },
                )
            }
          />
        </div>
      </section>
    </TooltipProvider>
  );
}

/** "+<digits>" for a DM whose JID or alias is a phone JID. */
function chatPhone(chat: ViewerChat | undefined, chatId: string): string | null {
  const ids = [chat?.jid, ...(Array.isArray(chat?.aliases) ? chat.aliases : []), chatId];
  const phone = ids.find((id) => id?.endsWith("@s.whatsapp.net"));
  return phone ? `+${phone.replace(/[:@].*$/, "")}` : null;
}

function ChatHeaderMenu({
  sessionId,
  chat,
  chatId,
}: {
  sessionId: string;
  chat: ViewerChat | undefined;
  chatId: string;
}) {
  const jid = chat?.jid ?? chatId;
  const phone = chatPhone(chat, chatId);
  const aliases = [jid, ...(Array.isArray(chat?.aliases) ? chat.aliases : [])];
  const lid = chat?.type === "dm" ? aliases.find((a) => a?.endsWith("@lid")) : undefined;
  const copy = (value: string, what: string) => {
    void copyText(value).then((ok) =>
      ok ? toast.success(`${what} copied`) : toast.error("Couldn't copy"),
    );
  };
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon-sm"
          className="shrink-0 text-muted-foreground"
          aria-label="Chat options"
        >
          <MoreVertical aria-hidden />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-60">
        <DropdownMenuLabel className="text-xs font-normal text-muted-foreground">
          Chat ID (use as <code className="font-mono">to</code> in the API)
          <span className="mt-0.5 block truncate font-mono text-foreground">{jid}</span>
        </DropdownMenuLabel>
        <DropdownMenuItem onClick={() => copy(jid, "Chat ID")}>
          <Copy /> Copy chat ID
        </DropdownMenuItem>
        {phone ? (
          <DropdownMenuItem onClick={() => copy(phone, "Phone number")}>
            <Phone /> Copy phone number
          </DropdownMenuItem>
        ) : null}
        {lid ? (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuItem asChild>
              <Link
                to="/user/sessions/$sessionId/contacts/$lid"
                params={{ sessionId, lid }}
              >
                <UserRound /> View contact
              </Link>
            </DropdownMenuItem>
          </>
        ) : null}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

function chatHeaderMeta(
  chat: ViewerChat | undefined,
  chatId: string,
  presenceState?: string,
): string {
  if (chat?.type === "dm") {
    if (presenceState === "available") return "Online";
    const phone = chatPhone(chat, chatId);
    return phone && prettyName(chat.name) !== phone ? phone : "Direct message";
  }
  if (chat?.type === "group") {
    const count = chat.participantCount
      ? `${chat.participantCount.toLocaleString()} members`
      : "Group";
    if (chat.isAnnounce) return `${count} · admins only`;
    return count;
  }
  if (chat?.type === "newsletter") return "Channel";
  if (chat?.type === "broadcast") return "Broadcast list";
  if (chat?.type === "status") return "Status updates";
  return chatId;
}

function TimelineBody({
  runs,
  isGroup,
  isLoading,
  isError,
  error,
  onRetry,
  hasOlder,
  isFetchingOlder,
  onLoadOlder,
  onReply,
  onVote,
  messagesById,
}: {
  runs: MessageRun[];
  isGroup: boolean;
  isLoading: boolean;
  isError: boolean;
  error: unknown;
  onRetry: () => void;
  hasOlder: boolean;
  isFetchingOlder: boolean;
  onLoadOlder: () => void;
  onReply: (message: Message) => void;
  onVote: (message: Message, options: string[]) => void;
  messagesById: Map<string, Message>;
}) {
  if (isLoading) {
    return (
      <div className="flex flex-1 items-center justify-center py-12 text-sm text-muted-foreground">
        <Spinner className="mr-2" />
        Loading messages…
      </div>
    );
  }

  if (isError) {
    return (
      <Empty className="border-0">
        <EmptyHeader>
          <EmptyTitle>Couldn't load messages</EmptyTitle>
          <EmptyDescription>
            {isApiError(error) ? error.message : "Something went wrong."}
          </EmptyDescription>
        </EmptyHeader>
        <Button size="sm" variant="outline" onClick={onRetry}>
          Retry
        </Button>
      </Empty>
    );
  }

  if (runs.length === 0) {
    return (
      <Empty className="border-0">
        <EmptyHeader>
          <EmptyMedia variant="icon" className="bg-background">
            <MessageSquare />
          </EmptyMedia>
          <EmptyTitle>No messages yet</EmptyTitle>
          <EmptyDescription>
            Say hello below to start this conversation.
          </EmptyDescription>
        </EmptyHeader>
      </Empty>
    );
  }

  const lastIndex = runs.length - 1;

  return (
    <>
      {hasOlder ? (
        <div className="flex justify-center">
          <Button
            size="xs"
            variant="ghost"
            className="rounded-full bg-background/70 px-3 text-muted-foreground shadow-xs ring-1 ring-border/60"
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
            <Marker className="my-2 justify-center">
              <MarkerContent className="rounded-full bg-background px-2.5 py-0.5 text-[11px] font-medium text-muted-foreground shadow-xs ring-1 ring-border/60">
                {run.dayHeading}
              </MarkerContent>
            </Marker>
          ) : null}
          <BubbleGroup className="gap-0.5">
            {run.messages.map((m, j) => (
              <MessageBubble
                key={messageId(m)}
                message={m}
                quotedMessage={
                  m.quotedMessageId ? messagesById.get(m.quotedMessageId) : undefined
                }
                showSender={isGroup && j === 0}
                tail={j === run.messages.length - 1}
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
      className="left-1/2 rounded-full bg-background px-3 text-xs font-medium shadow-md ring-1 ring-border/60"
    >
      <span className="flex items-center gap-1">
        <ChevronDown className="size-3.5" aria-hidden />
        Jump to latest
      </span>
    </MessageScrollerButton>
  );
}
