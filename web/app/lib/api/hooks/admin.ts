// Admin (api/v1) hooks: cross-org WhatsApp-session oversight against the gateway.
// User/org administration (list/ban/impersonate/roles) is not here — it goes
// through the better-auth admin client (authClient.admin, ~/lib/auth/client.ts).

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
