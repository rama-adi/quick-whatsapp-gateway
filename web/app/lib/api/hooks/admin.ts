// Admin (api/v1) hooks: cross-tenant session oversight.
// FROZEN — owned by the foundation agent. Authula tenant admin lives in
// ~/lib/auth/admin instead.

import {
  useInfiniteQuery,
  type InfiniteData,
  type UseInfiniteQueryResult,
} from "@tanstack/react-query";
import { qk } from "../../query";
import type { ApiError, Page } from "../envelope";
import type { WASession } from "../types";
import { listPageFetcher, nextCursor } from "./_shared";

export function useAdminSessions(): UseInfiniteQueryResult<
  InfiniteData<Page<WASession>, string | undefined>,
  ApiError
> {
  return useInfiniteQuery({
    queryKey: qk.adminSessions(),
    initialPageParam: undefined as string | undefined,
    queryFn: listPageFetcher<WASession>("/admin/sessions"),
    getNextPageParam: nextCursor,
  });
}
