// Viewer: one chat's message timeline + composer. Surface: viewer. Child of the
// chats list — renders in its <Outlet/>.
//
//   - SSR: the loader seeds the chat header (qk.chat) + page 0 of the message
//     timeline (qk.chatMessages) via Drizzle direct reads (-viewer-data.ts).
//   - Realtime: live inbound/outbound messages are prepended to page 0 of the
//     cache by the shared cacheBridge ("message"/"message.from_me"); statuses
//     patched by "message.status"; reactions/edits patch the body. We read the
//     cache + re-render.
//   - Display: flatten pages, sort ascending (oldest→newest), group by day.
//     Newest sits at the bottom; new arrivals auto-scroll there, and "Load
//     older" prepends without yanking the viewport.
//   - Composer: send goes through useSendMessage (optimistic), text only.
//   - Media is 501 in v1 → "not downloaded" placeholder bubble.

import {
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type FormEvent,
} from "react";
import { createFileRoute } from "@tanstack/react-router";
import type { InfiniteData } from "@tanstack/react-query";
import { SendHorizonal, MessageSquare } from "lucide-react";
import { useChatMessages, useSendMessage } from "~/lib/api/hooks/messages";
import { useChat, useMarkChatRead } from "~/lib/api/hooks/chats";
import { qk } from "~/lib/query";
import type { Chat, Message } from "~/lib/api/types";
import type { Page } from "~/lib/api/envelope";
import { isApiError } from "~/lib/api/envelope";
import { usePollingInterval } from "~/lib/events/useEventStream";
import { Button } from "~/components/ui/button";
import { Textarea } from "~/components/ui/textarea";
import { ScrollArea } from "~/components/ui/scroll-area";
import { Separator } from "~/components/ui/separator";
import { Spinner } from "~/components/ui/spinner";
import { TooltipProvider } from "~/components/ui/tooltip";
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
    await Promise.all([
      context.queryClient.ensureQueryData({
        queryKey: qk.chat(sessionId, chatId),
        queryFn: async (): Promise<Chat> => {
          const chat = await fetchChat({
            data: { sessionId, chatJid: chatId },
          });
          // ensureQueryData must resolve a value; an empty stub lets the client
          // hook refetch + the header render the jid until the gateway answers.
          return chat ?? { jid: chatId };
        },
      }),
      context.queryClient.ensureInfiniteQueryData({
        queryKey: qk.chatMessages(sessionId, chatId),
        initialPageParam: undefined as string | undefined,
        queryFn: () =>
          fetchMessagesPage({ data: { sessionId, chatJid: chatId } }),
        getNextPageParam: (last: Page<Message>) => last.nextCursor ?? undefined,
      }),
    ]);
  },
  component: ViewerTimeline,
});

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
    return [...flat].sort((a, b) => (a.timestamp ?? 0) - (b.timestamp ?? 0));
  }, [messages.data]);

  // Scroll management. Newest sits at the bottom: jump there on new arrivals,
  // but when "Load older" prepends a page keep the reading position fixed by
  // restoring the scroll offset from the bottom.
  const viewportRef = useRef<HTMLDivElement | null>(null);
  const bottomRef = useRef<HTMLDivElement | null>(null);
  const prependAnchor = useRef<number | null>(null);
  const oldestId = ordered.length > 0 ? ordered[0]?.id : undefined;
  const newestId =
    ordered.length > 0 ? ordered[ordered.length - 1]?.id : undefined;

  // New message at the bottom → scroll into view.
  useEffect(() => {
    if (prependAnchor.current !== null) return; // a prepend is in flight
    bottomRef.current?.scrollIntoView({ block: "end" });
  }, [newestId]);

  // Older page prepended → restore distance-from-bottom so the view doesn't jump.
  useLayoutEffect(() => {
    const vp = viewportRef.current;
    if (vp && prependAnchor.current !== null) {
      vp.scrollTop = vp.scrollHeight - prependAnchor.current;
      prependAnchor.current = null;
    }
  }, [oldestId]);

  const loadOlder = () => {
    const vp = viewportRef.current;
    if (vp) prependAnchor.current = vp.scrollHeight - vp.scrollTop;
    void messages.fetchNextPage();
  };

  const title = chat.data?.name || chat.data?.jid || chatId;
  const unread = chat.data?.unreadCount ?? 0;

  return (
    <TooltipProvider>
      <section
        aria-label="Conversation"
        className="flex h-[calc(100vh-12rem)] flex-col rounded-lg border bg-card"
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

        <ScrollArea viewportRef={viewportRef} className="flex-1">
          <div className="flex flex-col gap-1 p-4">
            <TimelineBody
              ordered={ordered}
              isLoading={messages.isLoading}
              isError={messages.isError}
              error={messages.error}
              hasOlder={Boolean(messages.hasNextPage)}
              isFetchingOlder={messages.isFetchingNextPage}
              onLoadOlder={loadOlder}
            />
            <div ref={bottomRef} />
          </div>
        </ScrollArea>

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
  ordered,
  isLoading,
  isError,
  error,
  hasOlder,
  isFetchingOlder,
  onLoadOlder,
}: {
  ordered: Message[];
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

  if (ordered.length === 0) {
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

  let lastDay = "";

  return (
    <>
      {hasOlder ? (
        <div className="flex justify-center pb-2">
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

      {ordered.map((m) => {
        const dk = dayKey(m.timestamp);
        const showDay = dk !== lastDay;
        lastDay = dk;
        return (
          <div key={m.id ?? `${m.timestamp}-${m.senderJid}`}>
            {showDay ? (
              <div className="my-3 flex items-center gap-3">
                <Separator className="flex-1" />
                <span className="shrink-0 rounded-full bg-muted px-3 py-1 text-xs text-muted-foreground">
                  {formatDayHeading(m.timestamp)}
                </span>
                <Separator className="flex-1" />
              </div>
            ) : null}
            <MessageBubble message={m} />
          </div>
        );
      })}
    </>
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
