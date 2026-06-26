// Webhook CRUD hooks. FROZEN — owned by the foundation agent.

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
import type { Webhook, WebhookRequest } from "../types";
import { apiUrl, fetchJSON, listPageFetcher, nextCursor } from "./_shared";

export function useWebhooks(): UseInfiniteQueryResult<
  InfiniteData<Page<Webhook>, string | undefined>,
  ApiError
> {
  return useInfiniteQuery({
    queryKey: qk.webhooks(),
    initialPageParam: undefined as string | undefined,
    queryFn: listPageFetcher<Webhook>("/webhooks"),
    getNextPageParam: nextCursor,
  });
}

export function useCreateWebhook(): UseMutationResult<
  Webhook,
  ApiError,
  WebhookRequest
> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body) =>
      fetchJSON<Webhook>(apiUrl("/webhooks"), {
        method: "POST",
        body: JSON.stringify(body),
      }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.webhooks() });
    },
  });
}

export function useUpdateWebhook(): UseMutationResult<
  Webhook,
  ApiError,
  { id: string; patch: WebhookRequest }
> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, patch }) =>
      fetchJSON<Webhook>(apiUrl(`/webhooks/${encodeURIComponent(id)}`), {
        method: "PATCH",
        body: JSON.stringify(patch),
      }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.webhooks() });
    },
  });
}

export function useDeleteWebhook(): UseMutationResult<void, ApiError, { id: string }> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id }) =>
      fetchJSON<void>(apiUrl(`/webhooks/${encodeURIComponent(id)}`), {
        method: "DELETE",
      }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.webhooks() });
    },
  });
}
