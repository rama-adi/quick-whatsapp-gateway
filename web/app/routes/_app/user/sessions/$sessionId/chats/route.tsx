// Viewer: the chat list. Master pane of the chats master/detail; the thread
// renders in the nested <Outlet/> (chats/$chatId), and chats/index renders the
// "no chat selected" pane.
//
//   - Layout: the list is a column flush to the inset panel's edges with a
//     hairline divider; the thread fills the rest. Below `md` the route decides
//     what shows: /chats is the list only, /chats/$chatId is the thread only
//     (with a back button in its header).
//   - The list loads through the client query so a slow database read cannot
//     block navigation in this route's loader.
//   - Realtime: the shared cacheBridge patches the same key on message/chat
//     events; we just read the cache (useChats). Polling fallback when degraded.

import { useEffect, useMemo, useState, type ReactNode } from "react";
import {
  createFileRoute,
  Link,
  Outlet,
  useParams,
} from "@tanstack/react-router";
import type { InfiniteData } from "@tanstack/react-query";
import { useQueryClient } from "@tanstack/react-query";
import {
  Archive,
  BellOff,
  Megaphone,
  MessageCirclePlus,
  MessagesSquare,
  Pin,
  QrCode,
  Search,
  SearchX,
  Users,
  WifiOff,
  X,
} from "lucide-react";
import { useChats } from "~/lib/api/hooks/chats";
import { useSession } from "~/lib/api/hooks/sessions";
import { qk } from "~/lib/query";
import type { Chat, WASession } from "~/lib/api/types";
import type { Page } from "~/lib/api/envelope";
import { isApiError } from "~/lib/api/envelope";
import { titleHead } from "~/lib/head";
import { usePollingInterval, useEventStream } from "~/lib/events/useEventStream";
import { useFillMain } from "~/components/shell/page-chrome";
import { Button } from "~/components/ui/button";
import { Input } from "~/components/ui/input";
import { Skeleton } from "~/components/ui/skeleton";
import { Tabs, TabsList, TabsTrigger } from "~/components/ui/tabs";
import { cn } from "~/lib/utils";
import { ChatAvatar, formatTimestamp, prettyName } from "./-viewer-ui";
import { NewChatDialog } from "./-new-chat";

export const Route = createFileRoute("/_app/user/sessions/$sessionId/chats")({
  head: () => titleHead("Chats"),
  component: ViewerChats,
});

type ChatFilter = "all" | "unread" | "groups" | "channels";

const FILTER_LABEL: Record<ChatFilter, string> = {
  all: "All",
  unread: "Unread",
  groups: "Groups",
  channels: "Channels",
};

/** A session that has never been linked to a phone (no number, never connected). */
export function isUnpaired(session: WASession | undefined): boolean {
  if (!session) return false;
  return session.status !== "working" && !session.phoneNumber && !session.lastConnectedAt;
}

function ViewerChats() {
  const { sessionId } = Route.useParams();
  // The chat surface manages its own internal scroll areas, so clamp <main> to
  // the viewport (the session-detail layout owns the top-bar back/tabs).
  useFillMain();
  // Read the active child param (chatId) to highlight the selected row and to
  // pick the visible pane on mobile; it lives on the nested thread route.
  const { chatId } = useParams({ strict: false });
  const chats = useChats(sessionId);
  const session = useSession(sessionId);
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

  const rows: Chat[] = useMemo(() => {
    const flat =
      (chats.data as InfiniteData<Page<Chat>> | undefined)?.pages.flatMap(
        (p) => p.data,
      ) ?? [];
    const byChat = new Map<string, Chat>();
    for (const chat of flat) {
      if (!chat.jid || !chat.lastMessageAt) continue;
      const key = chatIdentityKey(chat);
      const prev = byChat.get(key);
      if (!prev || (chat.lastMessageAt ?? 0) >= (prev.lastMessageAt ?? 0)) {
        byChat.set(key, mergeChatRows(prev, chat));
      }
    }
    return [...byChat.values()].sort(
      (a, b) => (b.lastMessageAt ?? 0) - (a.lastMessageAt ?? 0),
    );
  }, [chats.data]);

  const [filter, setFilter] = useState<ChatFilter>("all");
  const [query, setQuery] = useState("");
  const trimmedQuery = query.trim().toLowerCase();
  const filteredRows = useMemo(() => {
    return rows.filter((chat) => {
      if (filter === "unread" && (chat.unreadCount ?? 0) <= 0) return false;
      if (filter === "groups" && chat.type !== "group") return false;
      if (
        filter === "channels" &&
        chat.type !== "newsletter" &&
        chat.type !== "broadcast"
      ) {
        return false;
      }
      if (!trimmedQuery) return true;
      return `${chat.name ?? ""} ${chatAliases(chat).join(" ")}`
        .toLowerCase()
        .includes(trimmedQuery);
    });
  }, [filter, trimmedQuery, rows]);

  const degraded = status === "polling" || status === "reconnecting";
  const filtered = filter !== "all" || Boolean(trimmedQuery);
  const working = session.data?.status === "working";
  const clearFilters = () => {
    setFilter("all");
    setQuery("");
  };

  return (
    <div className="flex h-full min-h-0">
      <section
        aria-label="Chats"
        className={cn(
          "min-h-0 w-full flex-col md:flex md:w-[340px] md:shrink-0 md:border-r md:border-border/70 lg:w-[360px]",
          chatId ? "hidden" : "flex",
        )}
      >
        <header className="flex shrink-0 items-center justify-between gap-2 px-4 pt-4 pb-3">
          <div className="flex min-w-0 items-baseline gap-2">
            <h1 className="text-base font-semibold tracking-tight">Chats</h1>
            {rows.length > 0 ? (
              <span className="text-xs text-muted-foreground tabular-nums">
                {rows.length}
                {chats.hasNextPage ? "+" : ""}
              </span>
            ) : null}
          </div>
          <NewChatDialog
            sessionId={sessionId}
            canCheckNumbers={working}
            trigger={
              <Button
                size="sm"
                variant="outline"
                className="gap-1.5"
                aria-label="New chat"
              >
                <MessageCirclePlus aria-hidden />
                New chat
              </Button>
            }
          />
        </header>

        {degraded ? (
          <div className="mx-4 mb-3 flex items-center gap-2 rounded-lg bg-amber-500/10 py-1.5 pr-1.5 pl-2.5 text-xs text-amber-800 dark:text-amber-300">
            <WifiOff className="size-3.5 shrink-0" aria-hidden />
            <span className="min-w-0 flex-1 truncate">
              {status === "polling"
                ? "Live updates paused. Refreshing every few seconds."
                : "Reconnecting to live updates…"}
            </span>
            <Button
              size="xs"
              variant="ghost"
              className="h-6 shrink-0 px-2 text-xs text-amber-900 hover:bg-amber-500/15 dark:text-amber-200"
              onClick={reconnectNow}
            >
              Reconnect
            </Button>
          </div>
        ) : null}

        <div className="shrink-0 space-y-2 px-3 pb-2">
          <div className="relative">
            <Search
              className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground"
              aria-hidden
            />
            <Input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Escape") setQuery("");
              }}
              className="pr-8 pl-8"
              placeholder="Search chats"
              aria-label="Search chats"
            />
            {query ? (
              <button
                type="button"
                onClick={() => setQuery("")}
                className="absolute top-1/2 right-1.5 flex size-5 -translate-y-1/2 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
                aria-label="Clear search"
              >
                <X className="size-3.5" aria-hidden />
              </button>
            ) : null}
          </div>
          <Tabs value={filter} onValueChange={(v) => setFilter(v as ChatFilter)}>
            <TabsList className="grid w-full grid-cols-4">
              {(Object.keys(FILTER_LABEL) as ChatFilter[]).map((key) => (
                <TabsTrigger key={key} value={key} className="text-xs">
                  {FILTER_LABEL[key]}
                </TabsTrigger>
              ))}
            </TabsList>
          </Tabs>
          {trimmedQuery && chats.hasNextPage ? (
            <p className="px-1 text-xs text-muted-foreground">
              Searching loaded chats only.{" "}
              <button
                type="button"
                className="font-medium text-foreground underline-offset-2 hover:underline disabled:opacity-60"
                disabled={chats.isFetchingNextPage}
                onClick={() => void chats.fetchNextPage()}
              >
                {chats.isFetchingNextPage ? "Loading…" : "Load older chats"}
              </button>
            </p>
          ) : null}
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain px-2 pb-2 [scrollbar-width:thin]">
          <ChatListBody
            sessionId={sessionId}
            chatId={chatId}
            rows={filteredRows}
            isLoading={chats.isLoading}
            isError={chats.isError}
            error={chats.error}
            onRetry={() => void chats.refetch()}
            empty={
              <ChatListEmpty
                sessionId={sessionId}
                filtered={filtered}
                filterLabel={filter === "all" ? null : FILTER_LABEL[filter]}
                unpaired={isUnpaired(session.data)}
                working={working}
                onClear={clearFilters}
              />
            }
          />
          {chats.hasNextPage && !chats.isLoading ? (
            <div className="px-1 pt-2">
              <Button
                variant="ghost"
                size="sm"
                className="w-full text-muted-foreground"
                disabled={chats.isFetchingNextPage}
                onClick={() => void chats.fetchNextPage()}
              >
                {chats.isFetchingNextPage ? "Loading…" : "Load older chats"}
              </Button>
            </div>
          ) : null}
        </div>
      </section>

      <div
        className={cn(
          "min-h-0 min-w-0 flex-1 flex-col md:flex",
          chatId ? "flex" : "hidden",
        )}
      >
        <Outlet />
      </div>
    </div>
  );
}

function ChatListEmpty({
  sessionId,
  filtered,
  filterLabel,
  unpaired,
  working,
  onClear,
}: {
  sessionId: string;
  filtered: boolean;
  filterLabel: string | null;
  unpaired: boolean;
  working: boolean;
  onClear: () => void;
}) {
  if (filtered) {
    return (
      <EmptyBlock
        icon={<SearchX />}
        title="No chats match"
        description={
          filterLabel
            ? `Nothing in ${filterLabel} matches. Try another filter or search term.`
            : "Try a different name or number."
        }
        action={
          <Button size="sm" variant="outline" onClick={onClear}>
            {filterLabel ? "Clear filters" : "Clear search"}
          </Button>
        }
      />
    );
  }
  if (unpaired) {
    return (
      <EmptyBlock
        icon={<QrCode />}
        title="Link a phone to see chats"
        description="This session isn't linked to a WhatsApp number yet. Scan the QR code on the overview page to connect it."
        action={
          <Button size="sm" asChild>
            <Link to="/user/sessions/$sessionId" params={{ sessionId }}>
              Link a phone
            </Link>
          </Button>
        }
      />
    );
  }
  return (
    <EmptyBlock
      icon={<MessagesSquare />}
      title="No chats yet"
      description={
        working
          ? "New chats appear here as messages arrive. To load older conversations, import a backup from the overview page."
          : "This session isn't connected, so no new messages are arriving. Start it from the overview page."
      }
      action={
        <Button size="sm" variant="outline" asChild>
          <Link to="/user/sessions/$sessionId" params={{ sessionId }}>
            {working ? "Import a backup" : "Open overview"}
          </Link>
        </Button>
      }
    />
  );
}

function EmptyBlock({
  icon,
  title,
  description,
  action,
}: {
  icon: ReactNode;
  title: string;
  description: string;
  action?: ReactNode;
}) {
  return (
    <div className="flex flex-col items-center gap-3 px-6 py-12 text-center">
      <span className="flex size-10 items-center justify-center rounded-xl bg-muted text-muted-foreground ring-1 ring-border [&_svg]:size-5">
        {icon}
      </span>
      <div className="space-y-1">
        <p className="text-sm font-medium">{title}</p>
        <p className="text-xs text-pretty text-muted-foreground">{description}</p>
      </div>
      {action}
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
  onRetry,
  empty,
}: {
  sessionId: string;
  chatId: string | undefined;
  rows: Chat[];
  isLoading: boolean;
  isError: boolean;
  error: unknown;
  onRetry: () => void;
  empty: ReactNode;
}) {
  const queryClient = useQueryClient();

  if (isLoading) {
    return (
      <ul className="space-y-1 pt-1">
        {Array.from({ length: 7 }).map((_, i) => (
          <li key={i} className="flex items-center gap-3 px-2 py-2">
            <Skeleton className="size-10 shrink-0 rounded-full" />
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
      <div className="space-y-3 px-3 py-6 text-center text-sm">
        <p className="text-destructive">
          {isApiError(error) ? error.message : "Couldn't load chats."}
        </p>
        <Button size="sm" variant="outline" onClick={onRetry}>
          Retry
        </Button>
      </div>
    );
  }

  if (rows.length === 0) return <>{empty}</>;

  return (
    <ul className="space-y-0.5">
      {rows.map((chat) => {
        const selected = Boolean(chatId && chatAliases(chat).includes(chatId));
        const unread = chat.unreadCount ?? 0;
        const muted = Boolean(chat.mutedUntil && chat.mutedUntil > Date.now());
        return (
          <li key={chat.jid}>
            <Link
              to="/user/sessions/$sessionId/chats/$chatId"
              params={{ sessionId, chatId: chat.jid ?? "" }}
              preload={false}
              onClick={() => {
                if (chat.jid) {
                  queryClient.setQueryData(qk.chat(sessionId, chat.jid), chat);
                }
              }}
              className={cn(
                "flex items-center gap-3 rounded-lg px-2 py-2 text-left outline-none transition-colors duration-150 ease-soft focus-visible:ring-2 focus-visible:ring-ring/40",
                selected ? "bg-accent text-accent-foreground" : "hover:bg-muted/70",
              )}
              aria-current={selected ? "true" : undefined}
            >
              <ChatAvatar
                sessionId={sessionId}
                name={chat.name}
                jid={chat.jid}
                type={chat.type}
                className="size-10"
              />
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <span
                    className={cn(
                      "min-w-0 flex-1 truncate text-sm",
                      unread > 0 ? "font-semibold" : "font-medium",
                    )}
                  >
                    {prettyName(chat.name) || chatSubtitle(chat) || "Unknown"}
                  </span>
                  {chat.lastMessageAt ? (
                    <span
                      className={cn(
                        "shrink-0 text-[11px] tabular-nums",
                        unread > 0
                          ? "font-medium text-emerald-700 dark:text-emerald-400"
                          : "text-muted-foreground",
                      )}
                    >
                      {formatTimestamp(chat.lastMessageAt)}
                    </span>
                  ) : null}
                </div>
                <div className="mt-0.5 flex items-center gap-1.5 text-xs text-muted-foreground">
                  <ChatTypeIcon chat={chat} />
                  <span className="min-w-0 flex-1 truncate">{chatSubtitle(chat)}</span>
                  {chat.pinned ? (
                    <Pin className="size-3 shrink-0" aria-label="Pinned" />
                  ) : null}
                  {muted ? (
                    <BellOff className="size-3 shrink-0" aria-label="Muted" />
                  ) : null}
                  {chat.archived ? (
                    <Archive className="size-3 shrink-0" aria-label="Archived" />
                  ) : null}
                  {unread > 0 ? (
                    <span
                      className="ml-0.5 inline-flex h-[18px] min-w-[18px] shrink-0 items-center justify-center rounded-full bg-emerald-600 px-1.5 text-[10px] font-semibold text-white tabular-nums dark:bg-emerald-500 dark:text-emerald-950"
                      aria-label={`${unread} unread`}
                    >
                      {unread > 99 ? "99+" : unread}
                    </span>
                  ) : null}
                </div>
              </div>
            </Link>
          </li>
        );
      })}
    </ul>
  );
}

function ChatTypeIcon({ chat }: { chat: Chat }) {
  if (chat.type === "group") {
    return <Users className="size-3 shrink-0" aria-hidden />;
  }
  if (chat.type === "newsletter" || chat.type === "broadcast") {
    return <Megaphone className="size-3 shrink-0" aria-hidden />;
  }
  return null;
}

/** Second line of a chat row: the phone number for DMs, else the chat kind. */
function chatSubtitle(chat: Chat): string {
  if (chat.type === "dm") {
    const phone = chatAliases(chat).find((a) => a.endsWith("@s.whatsapp.net"));
    const number = phone ? `+${phone.replace(/[:@].*$/, "")}` : null;
    // Don't repeat the number when the name already is the number.
    if (number && prettyName(chat.name) !== number) return number;
    return "Direct message";
  }
  switch (chat.type) {
    case "group":
      return "Group";
    case "newsletter":
      return "Channel";
    case "broadcast":
      return "Broadcast list";
    case "status":
      return "Status updates";
    default:
      return "Chat";
  }
}

function chatIdentityKey(chat: Chat): string {
  const aliases = chatAliases(chat);
  if (aliases.length > 0) return aliases.sort().join("|");
  return chat.jid ?? "";
}

function chatAliases(chat: Chat): string[] {
  const aliases = Array.isArray(chat.aliases) ? chat.aliases : [];
  return [...new Set([chat.jid, ...aliases].filter(Boolean) as string[])];
}

function mergeChatRows(prev: Chat | undefined, next: Chat): Chat {
  if (!prev) return next;
  const aliases = [...new Set([...chatAliases(prev), ...chatAliases(next)])];
  const newer = (next.lastMessageAt ?? 0) >= (prev.lastMessageAt ?? 0) ? next : prev;
  return {
    ...prev,
    ...newer,
    aliases,
    unreadCount: Math.max(prev.unreadCount ?? 0, next.unreadCount ?? 0),
    pinned: Boolean(prev.pinned || next.pinned),
    archived: Boolean(prev.archived && next.archived),
    mutedUntil: next.mutedUntil ?? prev.mutedUntil,
    name: next.name ?? prev.name,
  };
}
