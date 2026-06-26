// Viewer: chats list (read-only). Surface agent: viewer. Parent of the timeline.
//
// Lists a session's chats via useChats() (cursor-paginated {data,nextCursor}).
// Selecting a chat renders the message timeline through the nested <Outlet/>.
// Live updates (new messages bump lastMessageAt / unread) arrive through the
// shared cacheBridge — we just read the cache. We also degrade to polling when
// the event stream drops (usePollingInterval feeds refetchInterval indirectly
// via a manual refetch effect).

import { useEffect } from "react";
import { NavLink, Outlet, useParams } from "react-router";
import { useChats } from "~/lib/api/hooks/chats";
import type { Chat } from "~/lib/api/types";
import { isApiError } from "~/lib/api/envelope";
import { usePollingInterval, useEventStream } from "~/lib/events/useEventStream";
import { Badge } from "~/components/ui/badge";
import { Button } from "~/components/ui/button";
import { Skeleton } from "~/components/ui/skeleton";
import { ScrollArea } from "~/components/ui/scroll-area";
import { cn } from "~/lib/utils";
import { ChatAvatar, formatTimestamp } from "./viewer-ui";

export default function ViewerChats() {
  const { sessionId = "", chatId } = useParams();
  const chats = useChats(sessionId);
  const pollMs = usePollingInterval();
  const { status, reconnectNow } = useEventStream();

  // Polling fallback: when the live stream is degraded, refetch the chat list on
  // an interval so unread counts / ordering stay reasonably fresh.
  useEffect(() => {
    if (!pollMs) return;
    const id = window.setInterval(() => {
      void chats.refetch();
    }, pollMs);
    return () => window.clearInterval(id);
  }, [pollMs, chats]);

  const rows: Chat[] = chats.data?.pages.flatMap((p) => p.data) ?? [];

  return (
    <div className="grid gap-4 md:grid-cols-[300px_1fr]">
      <section
        aria-label="Chats"
        className="flex max-h-[calc(100vh-12rem)] flex-col rounded-lg border bg-card"
      >
        <header className="flex items-center justify-between gap-2 border-b px-3 py-2">
          <h2 className="text-sm font-semibold">Chats</h2>
          {status === "polling" || status === "reconnecting" ? (
            <Button
              size="sm"
              variant="ghost"
              className="h-7 px-2 text-xs"
              onClick={reconnectNow}
              title="Live stream degraded — click to reconnect"
            >
              {status === "polling" ? "Polling" : "Reconnecting"}
            </Button>
          ) : null}
        </header>

        <ScrollArea className="flex-1">
          <ChatListBody
            sessionId={sessionId}
            chatId={chatId}
            rows={rows}
            isLoading={chats.isLoading}
            isError={chats.isError}
            error={chats.error}
          />
          {chats.hasNextPage ? (
            <div className="p-2">
              <Button
                variant="outline"
                size="sm"
                className="w-full"
                disabled={chats.isFetchingNextPage}
                onClick={() => void chats.fetchNextPage()}
              >
                {chats.isFetchingNextPage ? "Loading…" : "Load more"}
              </Button>
            </div>
          ) : null}
        </ScrollArea>
      </section>

      <div className="min-w-0">
        <Outlet />
      </div>
    </div>
  );
}

function ChatListBody({
  sessionId,
  chatId,
  rows,
  isLoading,
  isError,
  error,
}: {
  sessionId: string;
  chatId: string | undefined;
  rows: Chat[];
  isLoading: boolean;
  isError: boolean;
  error: unknown;
}) {
  if (isLoading) {
    return (
      <ul className="space-y-1 p-2">
        {Array.from({ length: 6 }).map((_, i) => (
          <li key={i} className="flex items-center gap-3 px-2 py-2">
            <Skeleton className="size-9 shrink-0 rounded-full" />
            <div className="flex-1 space-y-1.5">
              <Skeleton className="h-3.5 w-2/3" />
              <Skeleton className="h-3 w-1/3" />
            </div>
          </li>
        ))}
      </ul>
    );
  }

  if (isError) {
    return (
      <p className="p-4 text-sm text-destructive">
        {isApiError(error) ? error.message : "Failed to load chats."}
      </p>
    );
  }

  if (rows.length === 0) {
    return (
      <p className="p-4 text-sm text-muted-foreground">No chats yet.</p>
    );
  }

  return (
    <ul className="p-1">
      {rows.map((chat) => {
        const selected = chat.jid === chatId;
        return (
          <li key={chat.jid}>
            <NavLink
              to={`/user/sessions/${encodeURIComponent(sessionId)}/chats/${encodeURIComponent(chat.jid ?? "")}`}
              className={cn(
                "flex items-center gap-3 rounded-md px-2 py-2 text-left transition-colors",
                selected
                  ? "bg-accent text-accent-foreground"
                  : "hover:bg-muted",
              )}
              aria-current={selected ? "true" : undefined}
            >
              <ChatAvatar name={chat.name} jid={chat.jid} type={chat.type} />
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <span className="truncate text-sm font-medium">
                    {chat.name || chat.jid || "Unknown"}
                  </span>
                  {chat.pinned ? (
                    <span
                      className="text-xs text-muted-foreground"
                      title="Pinned"
                      aria-label="Pinned"
                    >
                      ◆
                    </span>
                  ) : null}
                </div>
                <div className="flex items-center gap-2 text-xs text-muted-foreground">
                  <span className="truncate">{chatTypeLabel(chat.type)}</span>
                  {chat.lastMessageAt ? (
                    <span className="shrink-0">
                      {formatTimestamp(chat.lastMessageAt)}
                    </span>
                  ) : null}
                </div>
              </div>
              {chat.unreadCount && chat.unreadCount > 0 ? (
                <Badge
                  variant="default"
                  className="shrink-0"
                  aria-label={`${chat.unreadCount} unread`}
                >
                  {chat.unreadCount > 99 ? "99+" : chat.unreadCount}
                </Badge>
              ) : null}
            </NavLink>
          </li>
        );
      })}
    </ul>
  );
}

function chatTypeLabel(type: Chat["type"]): string {
  switch (type) {
    case "dm":
      return "Direct";
    case "group":
      return "Group";
    case "newsletter":
      return "Newsletter";
    case "broadcast":
      return "Broadcast";
    case "status":
      return "Status";
    default:
      return "Chat";
  }
}
