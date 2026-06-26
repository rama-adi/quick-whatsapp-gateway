// API-key hooks (list/create/rotate/delete). Secrets are shown ONCE — never
// optimistic, never cached. FROZEN — owned by the foundation agent.

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
import type { ApiKey, CreateKeyRequest, CreateKeyResult } from "../types";
import { apiUrl, fetchJSON, listPageFetcher, nextCursor } from "./_shared";

export function useKeys(): UseInfiniteQueryResult<
  InfiniteData<Page<ApiKey>, string | undefined>,
  ApiError
> {
  return useInfiniteQuery({
    queryKey: qk.keys(),
    initialPageParam: undefined as string | undefined,
    queryFn: listPageFetcher<ApiKey>("/keys"),
    getNextPageParam: nextCursor,
  });
}

export function useCreateKey(): UseMutationResult<
  CreateKeyResult,
  ApiError,
  CreateKeyRequest
> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body) =>
      fetchJSON<CreateKeyResult>(apiUrl("/keys"), {
        method: "POST",
        body: JSON.stringify(body),
      }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.keys() });
    },
  });
}

export function useRotateKey(): UseMutationResult<
  CreateKeyResult,
  ApiError,
  { id: string }
> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id }) =>
      fetchJSON<CreateKeyResult>(apiUrl(`/keys/${encodeURIComponent(id)}:rotate`), {
        method: "POST",
      }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.keys() });
    },
  });
}

export function useDeleteKey(): UseMutationResult<void, ApiError, { id: string }> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id }) =>
      fetchJSON<void>(apiUrl(`/keys/${encodeURIComponent(id)}`), { method: "DELETE" }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.keys() });
    },
  });
}
