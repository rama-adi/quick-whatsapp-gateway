// Chat resource hooks (list/get + optimistic patch).
// FROZEN — owned by the foundation agent.

import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
  type InfiniteData,
  type UseInfiniteQueryResult,
  type UseMutationResult,
  type UseQueryResult,
} from "@tanstack/react-query";
import { qk } from "../../query";
import type { ApiError, Page } from "../envelope";
import type { Chat, UpdateChatRequest } from "../types";
import { apiUrl, fetchJSON, listPageFetcher, nextCursor } from "./_shared";

export function useChats(
  s: string,
): UseInfiniteQueryResult<InfiniteData<Page<Chat>, string | undefined>, ApiError> {
  return useInfiniteQuery({
    queryKey: qk.chats(s),
    enabled: Boolean(s),
    initialPageParam: undefined as string | undefined,
    queryFn: listPageFetcher<Chat>(`/sessions/${encodeURIComponent(s)}/chats`),
    getNextPageParam: nextCursor,
  });
}

export function useChat(s: string, c: string): UseQueryResult<Chat, ApiError> {
  return useQuery({
    queryKey: qk.chat(s, c),
    enabled: Boolean(s && c),
    queryFn: () =>
      fetchJSON<Chat>(
        apiUrl(`/sessions/${encodeURIComponent(s)}/chats/${encodeURIComponent(c)}`),
      ),
  });
}

/** Pin / mute / archive / unmute — optimistic against the cached chat. */
export function usePatchChat(
  s: string,
): UseMutationResult<Chat, ApiError, { chatId: string; patch: UpdateChatRequest }> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ chatId, patch }) =>
      fetchJSON<Chat>(
        apiUrl(`/sessions/${encodeURIComponent(s)}/chats/${encodeURIComponent(chatId)}`),
        { method: "PATCH", body: JSON.stringify(patch) },
      ),
    onMutate: async ({ chatId, patch }) => {
      await qc.cancelQueries({ queryKey: qk.chat(s, chatId) });
      const prev = qc.getQueryData<Chat>(qk.chat(s, chatId));
      if (prev) {
        qc.setQueryData<Chat>(qk.chat(s, chatId), { ...prev, ...patch });
      }
      return { prev };
    },
    onError: (_e, { chatId }, ctx) => {
      if (ctx?.prev) qc.setQueryData(qk.chat(s, chatId), ctx.prev);
    },
    onSuccess: (data, { chatId }) => {
      qc.setQueryData(qk.chat(s, chatId), data);
    },
  });
}

export function useMarkChatRead(
  s: string,
): UseMutationResult<void, ApiError, { chatId: string; messageIds?: string[] }> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ chatId, messageIds }) =>
      fetchJSON<void>(
        apiUrl(`/sessions/${encodeURIComponent(s)}/chats/${encodeURIComponent(chatId)}/read`),
        {
          method: "POST",
          body: JSON.stringify(messageIds ? { messageIds } : {}),
        },
      ),
    onSuccess: (_d, { chatId }) => {
      qc.setQueryData<Chat>(qk.chat(s, chatId), (cur) =>
        cur ? { ...cur, unreadCount: 0 } : cur,
      );
    },
  });
}
