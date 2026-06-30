// Viewer: chats list (read-only). Surface: viewer. Master pane of the viewer
// master/detail; the timeline renders in the nested <Outlet/>.
//
// Ported from the v1 react-router route to TanStack Start idioms:
//   - SSR: the route `loader` seeds page 0 of the chats list into the SAME
//     TanStack Query cache key the client hook uses (qk.chats(sessionId)), via
//     a Drizzle direct read (viewer.server.ts, §6.2). The client hook then
//     hydrates from cache instead of refetching on mount.
//   - Realtime: the shared cacheBridge patches the same key on message/chat
//     events; we just read the cache (useChats). Polling fallback when degraded.
//   - useParams() -> Route.useParams(); NavLink -> @tanstack/react-router Link
//     with activeProps; <Outlet/> from @tanstack/react-router.

import { useEffect, useMemo, useState } from "react";
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
  Bell,
  Megaphone,
  MessageCirclePlus,
  Pin,
  Search,
  Users,
} from "lucide-react";
import { useChats } from "~/lib/api/hooks/chats";
import { useContacts } from "~/lib/api/hooks/contacts";
import { qk } from "~/lib/query";
import type { Chat, Contact } from "~/lib/api/types";
import type { Page } from "~/lib/api/envelope";
import { isApiError } from "~/lib/api/envelope";
import { usePollingInterval, useEventStream } from "~/lib/events/useEventStream";
import { useFillMain } from "~/components/shell/page-chrome";
import { Badge } from "~/components/ui/badge";
import { Button } from "~/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "~/components/ui/dialog";
import { Input } from "~/components/ui/input";
import { Skeleton } from "~/components/ui/skeleton";
import { ScrollArea } from "~/components/ui/scroll-area";
import { Tabs, TabsList, TabsTrigger } from "~/components/ui/tabs";
import { cn } from "~/lib/utils";
import { ChatAvatar, formatTimestamp } from "./-viewer-ui";
import { fetchChatsPage } from "./-viewer-data";

export const Route = createFileRoute("/_app/user/sessions/$sessionId/chats")({
  loader: async ({ params, context }) => {
    // Seed page 0 into the cache under the canonical hook key so the client
    // hydrates from SSR and the cacheBridge has a page to patch.
    try {
      await context.queryClient.ensureInfiniteQueryData({
        queryKey: qk.chats(params.sessionId),
        initialPageParam: undefined as string | undefined,
        queryFn: () =>
          fetchChatsPage({ data: { sessionId: params.sessionId } }),
        getNextPageParam: (last: Page<Chat>) => last.nextCursor ?? undefined,
      });
    } catch (err) {
      console.warn("[viewer] failed to preload chats:", err);
    }
  },
  component: ViewerChats,
});

function ViewerChats() {
  const { sessionId } = Route.useParams();
  // The chat surface manages its own internal scroll areas, so clamp <main> to
  // the viewport (the session-detail layout owns the top-bar back/tabs).
  useFillMain();
  // Read the active child param (chatId) to highlight the selected row; it lives
  // on the nested timeline route, so read the merged params non-strictly.
  const { chatId } = useParams({ strict: false });
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

  const [filter, setFilter] = useState<"all" | "unread" | "groups" | "channels">(
    "all",
  );
  const [query, setQuery] = useState("");
  const filteredRows = useMemo(() => {
    const q = query.trim().toLowerCase();
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
      if (!q) return true;
      return `${chat.name ?? ""} ${chat.jid ?? ""}`.toLowerCase().includes(q);
    });
  }, [filter, query, rows]);

  const degraded = status === "polling" || status === "reconnecting";

  return (
    <div className="grid h-full min-h-0 gap-4 overflow-y-auto p-4 md:grid-cols-[360px_1fr] md:grid-rows-[minmax(0,1fr)] md:overflow-hidden">
      <section
        aria-label="Chats"
        className="flex max-h-[45svh] min-h-0 flex-col overflow-hidden rounded-lg border bg-card md:max-h-none"
      >
        <header className="flex items-center justify-between gap-2 border-b px-3 py-2">
          <div className="min-w-0">
            <h1 className="text-sm font-semibold">Chats</h1>
            {degraded ? (
              <p className="truncate text-xs text-muted-foreground">
                Live updates {status === "polling" ? "paused" : "reconnecting..."}
              </p>
            ) : null}
          </div>
          <div className="flex items-center gap-1">
            {degraded ? (
              <Button
                size="sm"
                variant="ghost"
                className="h-8 px-2 text-xs"
                onClick={reconnectNow}
                title="Reconnect the live stream"
              >
                Reconnect
              </Button>
            ) : null}
            <NewChatDialog sessionId={sessionId} />
          </div>
        </header>

        <div className="space-y-2 border-b p-2">
          <div className="relative">
            <Search
              className="pointer-events-none absolute left-2 top-1/2 size-4 -translate-y-1/2 text-muted-foreground"
              aria-hidden
            />
            <Input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              className="h-9 pl-8"
              placeholder="Search chats"
              aria-label="Search chats"
            />
          </div>
          <Tabs value={filter} onValueChange={(v) => setFilter(v as typeof filter)}>
            <TabsList className="grid h-auto w-full grid-cols-4">
              <TabsTrigger value="all" className="h-8 text-xs">
                All
              </TabsTrigger>
              <TabsTrigger value="unread" className="h-8 text-xs">
                Unread
              </TabsTrigger>
              <TabsTrigger value="groups" className="h-8 text-xs">
                Groups
              </TabsTrigger>
              <TabsTrigger value="channels" className="h-8 text-xs">
                Channels
              </TabsTrigger>
            </TabsList>
          </Tabs>
        </div>

        <ScrollArea className="min-h-0 flex-1">
          <ChatListBody
            sessionId={sessionId}
            chatId={chatId}
            rows={filteredRows}
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

      <div className="min-h-0 min-w-0">
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
      <p className="p-4 text-sm text-muted-foreground">
        No chats match this view.
      </p>
    );
  }

  return (
    <ul className="p-1">
      {rows.map((chat) => {
        const selected = Boolean(chatId && chatAliases(chat).includes(chatId));
        return (
          <li key={chat.jid}>
            <Link
              to="/user/sessions/$sessionId/chats/$chatId"
              params={{ sessionId, chatId: chat.jid ?? "" }}
              className={cn(
                "flex items-center gap-3 rounded-md px-2 py-2 text-left transition-colors",
                selected
                  ? "bg-accent text-accent-foreground"
                  : "hover:bg-muted",
              )}
              aria-current={selected ? "true" : undefined}
            >
              <ChatAvatar
                sessionId={sessionId}
                name={chat.name}
                jid={chat.jid}
                type={chat.type}
              />
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <span className="truncate text-sm font-medium">
                    {chat.name || chat.jid || "Unknown"}
                  </span>
                  <ChatTypeIcon chat={chat} />
                  {chat.pinned ? <Pin className="size-3 text-muted-foreground" /> : null}
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
              {chat.archived ? (
                <Archive
                  className="size-3.5 shrink-0 text-muted-foreground"
                  aria-label="Archived"
                />
              ) : null}
              {chat.mutedUntil ? (
                <Bell
                  className="size-3.5 shrink-0 text-muted-foreground"
                  aria-label="Muted"
                />
              ) : null}
              {chat.unreadCount && chat.unreadCount > 0 ? (
                <Badge
                  variant="default"
                  className="shrink-0"
                  aria-label={`${chat.unreadCount} unread`}
                >
                  {chat.unreadCount > 99 ? "99+" : chat.unreadCount}
                </Badge>
              ) : null}
            </Link>
          </li>
        );
      })}
    </ul>
  );
}

function ChatTypeIcon({ chat }: { chat: Chat }) {
  if (chat.type === "group") {
    return <Users className="size-3.5 shrink-0 text-muted-foreground" aria-label="Group" />;
  }
  if (chat.type === "newsletter") {
    return (
      <Megaphone
        className="size-3.5 shrink-0 text-muted-foreground"
        aria-label="Announcement channel"
      />
    );
  }
  if (chat.type === "broadcast") {
    return (
      <Megaphone
        className="size-3.5 shrink-0 text-muted-foreground"
        aria-label="Broadcast list"
      />
    );
  }
  return null;
}

function NewChatDialog({ sessionId }: { sessionId: string }) {
  const [open, setOpen] = useState(false);
  const [q, setQ] = useState("");
  const qc = useQueryClient();
  const contacts = useContacts(sessionId, { q: q || undefined });
  const rows = useMemo(() => {
    const flat =
      (contacts.data as InfiniteData<Page<Contact>> | undefined)?.pages.flatMap(
        (p) => p.data,
      ) ?? [];
    return flat;
  }, [contacts.data]);

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button
          size="icon"
          variant="ghost"
          className="size-8"
          aria-label="New chat"
          title="New chat"
        >
          <MessageCirclePlus className="size-4" aria-hidden />
        </Button>
      </DialogTrigger>
      <DialogContent className="p-0 sm:max-w-md">
        <DialogHeader className="border-b px-4 py-3">
          <DialogTitle className="text-base">New chat</DialogTitle>
          <DialogDescription>
            Pick a found contact to start a direct conversation.
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-3 p-3">
          <Input
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder="Search contacts"
            aria-label="Search contacts"
          />
          <ScrollArea className="h-72">
            <NewChatRows
              sessionId={sessionId}
              rows={rows}
              isLoading={contacts.isLoading}
              isError={contacts.isError}
              onPick={(contact) => {
                const chatId = contact.lid;
                qc.setQueryData<Chat>(qk.chat(sessionId, chatId), {
                  id: 0,
                  sessionId,
                  jid: chatId,
                  type: "dm",
                  name: contactDisplayName(contact),
                  unreadCount: 0,
                  archived: false,
                  pinned: false,
                });
                setOpen(false);
              }}
            />
            {contacts.hasNextPage ? (
              <div className="p-2">
                <Button
                  variant="outline"
                  size="sm"
                  className="w-full"
                  disabled={contacts.isFetchingNextPage}
                  onClick={() => void contacts.fetchNextPage()}
                >
                  {contacts.isFetchingNextPage ? "Loading..." : "Load more"}
                </Button>
              </div>
            ) : null}
          </ScrollArea>
        </div>
      </DialogContent>
    </Dialog>
  );
}

function NewChatRows({
  sessionId,
  rows,
  isLoading,
  isError,
  onPick,
}: {
  sessionId: string;
  rows: Contact[];
  isLoading: boolean;
  isError: boolean;
  onPick: (contact: Contact) => void;
}) {
  if (isLoading) {
    return (
      <ul className="space-y-1 p-1">
        {Array.from({ length: 5 }).map((_, i) => (
          <li key={i} className="flex items-center gap-3 px-2 py-2">
            <Skeleton className="size-9 shrink-0 rounded-full" />
            <Skeleton className="h-4 flex-1" />
          </li>
        ))}
      </ul>
    );
  }
  if (isError) {
    return <p className="p-3 text-sm text-destructive">Failed to load contacts.</p>;
  }
  if (rows.length === 0) {
    return (
      <p className="p-3 text-sm text-muted-foreground">
        No discovered contacts found.
      </p>
    );
  }
  return (
    <ul className="p-1">
      {rows.map((contact) => (
        <li key={contact.lid}>
          <Link
            to="/user/sessions/$sessionId/chats/$chatId"
            params={{ sessionId, chatId: contact.lid }}
            onClick={() => onPick(contact)}
            className="flex items-center gap-3 rounded-md px-2 py-2 text-left transition-colors hover:bg-muted"
          >
            <ChatAvatar
              sessionId={sessionId}
              name={contactDisplayName(contact)}
              jid={contact.lid}
              type="dm"
            />
            <div className="min-w-0 flex-1">
              <div className="truncate text-sm font-medium">
                {contactDisplayName(contact)}
              </div>
              <div className="truncate text-xs text-muted-foreground">
                {contact.phoneNumber || contact.lid}
              </div>
            </div>
          </Link>
        </li>
      ))}
    </ul>
  );
}

function contactDisplayName(contact: Contact): string {
  return (
    contact.name ||
    contact.businessName ||
    contact.phoneNumber ||
    contact.lid ||
    "Unknown"
  );
}

function chatTypeLabel(type: Chat["type"]): string {
  switch (type) {
    case "dm":
      return "Direct";
    case "group":
      return "Group";
    case "newsletter":
      return "Announcement channel";
    case "broadcast":
      return "Broadcast";
    case "status":
      return "Status";
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
