// Message hooks: paginated timeline + optimistic send.
// FROZEN — owned by the foundation agent.
//
// Optimistic send (canonical pattern):
//   - send Idempotency-Key: crypto.randomUUID()
//   - onMutate prepends a tmp_* Message (status:"pending") to page 0 of the
//     chat timeline keyed by SendRequest.to
//   - onError rolls back
//   - onSuccess reconciles tmp id → real messageId
//   - the stream's message.status event (keyed by messageId) is authoritative.

import {
  useInfiniteQuery,
  useMutation,
  useQueryClient,
  type InfiniteData,
  type UseInfiniteQueryResult,
  type UseMutationResult,
} from "@tanstack/react-query";
import { qk } from "../../query";
import type { ApiError, Page } from "../envelope";
import type { Chat, Message, SendRequest, SendResult } from "../types";
import { apiUrl, fetchJSON, listPageFetcher, nextCursor } from "./_shared";

export function useChatMessages(
  s: string,
  c: string,
): UseInfiniteQueryResult<
  InfiniteData<Page<Message>, string | undefined>,
  ApiError
> {
  return useInfiniteQuery({
    queryKey: qk.chatMessages(s, c),
    enabled: Boolean(s && c),
    initialPageParam: undefined as string | undefined,
    queryFn: listPageFetcher<Message>(
      `/sessions/${encodeURIComponent(s)}/chats/${encodeURIComponent(c)}/messages`,
    ),
    getNextPageParam: nextCursor,
  });
}

type Infinite = InfiniteData<Page<Message>, string | undefined>;

export function useSendMessage(
  s: string,
): UseMutationResult<
  SendResult,
  ApiError,
  SendRequest,
  { tmpId: string; chatJid: string }
> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body) =>
      fetchJSON<SendResult>(apiUrl(`/sessions/${encodeURIComponent(s)}/messages`), {
        method: "POST",
        headers: { "Idempotency-Key": crypto.randomUUID() },
        body: JSON.stringify(body),
      }),
    onMutate: async (body) => {
      const chatJid = body.to;
      const key = qk.chatMessages(s, chatJid);
      await qc.cancelQueries({ queryKey: key });
      const tmpId = `tmp_${crypto.randomUUID()}`;
      const now = Date.now();
      const optimistic: Message = {
        id: tmpId,
        sessionId: s,
        chatJid,
        direction: "out",
        fromMe: true,
        type: body.type,
        body: body.text ?? "",
        status: "pending",
        timestamp: now,
        createdAt: now,
        deleted: false,
        edited: false,
        hasMedia: false,
        waMessageId: "",
      };
      qc.setQueryData<Chat>(qk.chat(s, chatJid), (cur) =>
        cur
          ? { ...cur, lastMessageAt: now }
          : {
              id: 0,
              sessionId: s,
              jid: chatJid,
              type: "dm",
              unreadCount: 0,
              archived: false,
              pinned: false,
              lastMessageAt: now,
            },
      );
      qc.setQueryData<InfiniteData<Page<Chat>, string | undefined>>(
        qk.chats(s),
        (data) => {
          if (!data || data.pages.length === 0) return data;
          const header = qc.getQueryData<Chat>(qk.chat(s, chatJid));
          let found = false;
          const pages = data.pages.map((page) => ({
            ...page,
            data: page.data.map((chat) => {
              if (chat.jid !== chatJid) return chat;
              found = true;
              return { ...chat, lastMessageAt: now };
            }),
          }));
          const [first, ...rest] = pages;
          if (!first) return data;
          const firstRows = found
            ? first.data
            : [
                {
                  ...(header ?? {
                    id: 0,
                    sessionId: s,
                    jid: chatJid,
                    type: "dm",
                    unreadCount: 0,
                    archived: false,
                    pinned: false,
                  }),
                  lastMessageAt: now,
                } satisfies Chat,
                ...first.data,
              ];
          return {
            ...data,
            pages: [
              {
                ...first,
                data: [...firstRows].sort(
                  (a, b) => (b.lastMessageAt ?? 0) - (a.lastMessageAt ?? 0),
                ),
              },
              ...rest,
            ],
          };
        },
      );
      qc.setQueryData<Infinite>(key, (data) => {
        if (!data || data.pages.length === 0) {
          return {
            pageParams: [undefined],
            pages: [{ data: [optimistic], nextCursor: null }],
          };
        }
        const [first, ...rest] = data.pages;
        if (!first) return data;
        return {
          ...data,
          pages: [{ ...first, data: [optimistic, ...first.data] }, ...rest],
        };
      });
      return { tmpId, chatJid };
    },
    onError: (_e, _body, ctx) => {
      if (!ctx) return;
      const key = qk.chatMessages(s, ctx.chatJid);
      qc.setQueryData<Infinite>(key, (data) => {
        if (!data) return data;
        return {
          ...data,
          pages: data.pages.map((p) => ({
            ...p,
            data: p.data.filter((m) => m.id !== ctx.tmpId),
          })),
        };
      });
    },
    onSuccess: (result, _body, ctx) => {
      if (!ctx) return;
      const key = qk.chatMessages(s, ctx.chatJid);
      const realId = result.waMessageId;
      qc.setQueryData<Infinite>(key, (data) => {
        if (!data) return data;
        // If the stream echo (message.from_me) already inserted the real
        // message before this resolved, drop the optimistic tmp instead of
        // renaming it — renaming would leave two rows sharing the real id.
        const realExists =
          realId != null &&
          data.pages.some((p) => p.data.some((m) => m.id === realId));
        return {
          ...data,
          pages: data.pages.map((p) => ({
            ...p,
            data: realExists
              ? p.data.filter((m) => m.id !== ctx.tmpId)
              : p.data.map((m) =>
                  m.id === ctx.tmpId
                    ? {
                        ...m,
                        id: realId ?? m.id,
                        status: (result.status as Message["status"]) ?? m.status,
                        timestamp: result.timestamp ?? m.timestamp,
                      }
                    : m,
                ),
          })),
        };
      });
    },
  });
}
