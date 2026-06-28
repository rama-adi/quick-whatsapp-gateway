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
//   - Composer: send goes through useSendMessage (optimistic), text only.

import { useEffect, useMemo, useState, type FormEvent } from "react";
import { createFileRoute } from "@tanstack/react-router";
import type { InfiniteData } from "@tanstack/react-query";
import { SendHorizonal, MessageSquare, ChevronDown } from "lucide-react";
import {
  MessageScroller,
  MessageScrollerProvider,
  MessageScrollerViewport,
  MessageScrollerContent,
  MessageScrollerItem,
  MessageScrollerButton,
  useMessageScrollerScrollable,
} from "~/components/ui/message-scroller";
import { useChatMessages, useSendMessage } from "~/lib/api/hooks/messages";
import { useChat, useMarkChatRead } from "~/lib/api/hooks/chats";
import { qk } from "~/lib/query";
import type { Chat, Message } from "~/lib/api/types";
import type { Page } from "~/lib/api/envelope";
import { isApiError } from "~/lib/api/envelope";
import { usePollingInterval } from "~/lib/events/useEventStream";
import { Button } from "~/components/ui/button";
import { Textarea } from "~/components/ui/textarea";
import { Spinner } from "~/components/ui/spinner";
import { TooltipProvider } from "~/components/ui/tooltip";
import { Marker, MarkerContent } from "~/components/ui/marker";
import { BubbleGroup } from "~/components/ui/bubble";
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
  return m.id ?? `${m.timestamp}-${senderKey(m)}`;
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
  const pollMs = usePollingInterval();

  // Polling fallback when the live stream is degraded.
  useEffect(() => {
    if (!pollMs) return;
    const id = window.setInterval(() => {
      void messages.refetch();
    }, pollMs);
    return () => window.clearInterval(id);
  }, [pollMs, messages]);

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

  // "Load older" feeds the existing infinite-query. MessageScroller's
  // preserveScrollOnPrepend keeps the reading position fixed when the prepended
  // page lands, so no manual scroll-offset bookkeeping is needed here.
  const canLoadOlder = Boolean(messages.hasNextPage);
  const isFetchingOlder = messages.isFetchingNextPage;
  const loadOlder = () => {
    if (canLoadOlder && !isFetchingOlder) void messages.fetchNextPage();
  };

  const title = chat.data?.name || chat.data?.jid || chatId;
  const unread = chat.data?.unreadCount ?? 0;

  return (
    <TooltipProvider>
      <section
        aria-label="Conversation"
        className="flex h-full min-h-[60svh] flex-col rounded-lg border bg-card md:min-h-0"
      >
        <header className="flex items-center gap-3 border-b px-4 py-2">
          <ChatAvatar
            name={chat.data?.name}
            jid={chat.data?.jid}
            type={chat.data?.type}
          />
          <div className="min-w-0 flex-1">
            <h2 className="truncate text-sm font-semibold">{title}</h2>
            <p className="truncate text-xs text-muted-foreground">
              {chat.data?.jid ?? chatId}
            </p>
          </div>
          <Button
            size="sm"
            variant="outline"
            disabled={markRead.isPending || unread === 0}
            onClick={() =>
              markRead.mutate(
                { chatId },
                {
                  onSuccess: () => toast.success("Marked as read"),
                  onError: (e) =>
                    toast.error(
                      isApiError(e) ? e.message : "Failed to mark read",
                    ),
                },
              )
            }
          >
            {unread > 0 ? `Mark read (${unread})` : "Read"}
          </Button>
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
                />
              </MessageScrollerContent>
            </MessageScrollerViewport>

            <JumpToLatest />
          </MessageScroller>
        </MessageScrollerProvider>

        <Composer
          disabled={send.isPending}
          onSend={(text) =>
            send.mutate(
              { to: chatId, type: "text", text },
              {
                onError: (e) =>
                  toast.error(isApiError(e) ? e.message : "Failed to send"),
              },
            )
          }
        />
      </section>
    </TooltipProvider>
  );
}

function TimelineBody({
  runs,
  isLoading,
  isError,
  error,
  hasOlder,
  isFetchingOlder,
  onLoadOlder,
}: {
  runs: MessageRun[];
  isLoading: boolean;
  isError: boolean;
  error: unknown;
  hasOlder: boolean;
  isFetchingOlder: boolean;
  onLoadOlder: () => void;
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
                showSender={j === 0}
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
  onSend,
}: {
  disabled: boolean;
  onSend: (text: string) => void;
}) {
  const [text, setText] = useState("");

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const trimmed = text.trim();
    if (!trimmed) return;
    onSend(trimmed);
    setText("");
  };

  return (
    <form
      onSubmit={submit}
      className="flex items-end gap-2 border-t p-3"
      aria-label="Send a message"
    >
      <Textarea
        value={text}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && !e.shiftKey) {
            e.preventDefault();
            (e.currentTarget.form as HTMLFormElement | null)?.requestSubmit();
          }
        }}
        placeholder="Type a message"
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
    </form>
  );
}
