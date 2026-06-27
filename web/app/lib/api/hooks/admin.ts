// Admin (api/v1) hooks: cross-org WhatsApp-session oversight against the gateway.
// User/org administration (list/ban/impersonate/roles) is not here — it goes
// through the better-auth admin client (authClient.admin, ~/lib/auth/client.ts).

import {
  useMutation,
  useInfiniteQuery,
  useQuery,
  useQueryClient,
  type InfiniteData,
  type UseInfiniteQueryResult,
} from "@tanstack/react-query";
import { qk } from "../../query";
import type { ApiError, Page } from "../envelope";
import type { BackfillJob, WASession } from "../types";
import { apiUrl, fetchJSON, listPageFetcher, nextCursor } from "./_shared";

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

const backfillKey = (sessionId: string) =>
  ["admin", "sessions", sessionId, "backfill"] as const;

export function useAdminSessionBackfill(sessionId: string) {
  return useQuery<BackfillJob, ApiError>({
    queryKey: backfillKey(sessionId),
    enabled: Boolean(sessionId),
    refetchInterval: (q) => (q.state.data?.status === "running" ? 1500 : false),
    queryFn: () =>
      fetchJSON<BackfillJob>(
        apiUrl(`/admin/sessions/${encodeURIComponent(sessionId)}/backfill`),
      ),
    retry: false,
  });
}

export function useStartAdminSessionBackfill() {
  const qc = useQueryClient();
  return useMutation<BackfillJob, ApiError, string>({
    mutationFn: (sessionId) =>
      fetchJSON<BackfillJob>(
        apiUrl(`/admin/sessions/${encodeURIComponent(sessionId)}:backfill`),
        { method: "POST" },
      ),
    onSuccess: (job) => {
      qc.setQueryData(backfillKey(job.sessionId), job);
    },
  });
}
