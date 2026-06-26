// Viewer: message timeline (read-only). Surface agent: viewer.
//
// Reads useChatMessages(session, chat) — cursor-paginated, page 0 = newest;
// live inbound/outbound messages are prepended to the cache by the shared
// cacheBridge (event "message" / "message.from_me") and statuses patched by
// "message.status", so this view re-renders automatically. We flatten + sort
// ascending for display (oldest → newest), group by day, and expose a
// "Load older" control that drives fetchNextPage.
//
// Mark-as-read is offered as an optional action (POST /chats/{cid}/read).
// Media is 501 in v1 → rendered as a graceful "not downloaded" placeholder.

import { useEffect, useMemo, useRef } from "react";
import { useParams } from "react-router";
import { useChatMessages } from "~/lib/api/hooks/messages";
import { useChat, useMarkChatRead } from "~/lib/api/hooks/chats";
import type { Message } from "~/lib/api/types";
import { isApiError } from "~/lib/api/envelope";
import { usePollingInterval } from "~/lib/events/useEventStream";
import { Button } from "~/components/ui/button";
import { Skeleton } from "~/components/ui/skeleton";
import { ScrollArea } from "~/components/ui/scroll-area";
import { toast } from "sonner";
import { ChatAvatar, formatDayHeading, dayKey } from "./viewer-ui";
import { MessageBubble } from "./message-bubble";

export default function ViewerTimeline() {
  const { sessionId = "", chatId = "" } = useParams();
  const chat = useChat(sessionId, chatId);
  const messages = useChatMessages(sessionId, chatId);
  const markRead = useMarkChatRead(sessionId);
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
    const flat = messages.data?.pages.flatMap((p) => p.data) ?? [];
    return [...flat].sort(
      (a, b) => (a.timestamp ?? 0) - (b.timestamp ?? 0),
    );
  }, [messages.data]);

  // Auto-scroll to bottom when the newest message changes (new arrivals).
  const bottomRef = useRef<HTMLDivElement | null>(null);
  const lastId = ordered.length > 0 ? ordered[ordered.length - 1]?.id : undefined;
  useEffect(() => {
    bottomRef.current?.scrollIntoView({ block: "end" });
  }, [lastId]);

  const title = chat.data?.name || chat.data?.jid || chatId;
  const unread = chat.data?.unreadCount ?? 0;

  return (
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

      <ScrollArea className="flex-1">
        <div className="flex flex-col gap-1 p-4">
          <TimelineBody
            ordered={ordered}
            isLoading={messages.isLoading}
            isError={messages.isError}
            error={messages.error}
            hasOlder={Boolean(messages.hasNextPage)}
            isFetchingOlder={messages.isFetchingNextPage}
            onLoadOlder={() => void messages.fetchNextPage()}
          />
          <div ref={bottomRef} />
        </div>
      </ScrollArea>
    </section>
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
      <div className="space-y-3">
        {Array.from({ length: 5 }).map((_, i) => (
          <Skeleton
            key={i}
            className={i % 2 === 0 ? "h-12 w-2/3" : "h-12 w-1/2 self-end ml-auto"}
          />
        ))}
      </div>
    );
  }

  if (isError) {
    return (
      <p className="py-8 text-center text-sm text-destructive">
        {isApiError(error) ? error.message : "Failed to load messages."}
      </p>
    );
  }

  if (ordered.length === 0) {
    return (
      <p className="py-12 text-center text-sm text-muted-foreground">
        No messages in this conversation yet.
      </p>
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
              <div className="my-3 flex justify-center">
                <span className="rounded-full bg-muted px-3 py-1 text-xs text-muted-foreground">
                  {formatDayHeading(m.timestamp)}
                </span>
              </div>
            ) : null}
            <MessageBubble message={m} />
          </div>
        );
      })}
    </>
  );
}
