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
import type { components } from "../schema";
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

// Gateway registry administration deliberately uses local query keys rather than
// placing enrollment responses in the cache: those responses contain a
// plaintext, single-use bearer and must remain in the component that received it.
type Gateway = components["schemas"]["GatewayAdmin"];
type GatewayDetail = components["schemas"]["GatewayAdminDetail"];
type GatewayInput = components["schemas"]["GatewayAdminBody"];
type Enrollment = components["schemas"]["GatewayEnrollmentResult"];

const gatewaysKey = ["admin", "gateways"] as const;
const gatewayKey = (gatewayId: string) => ["admin", "gateways", gatewayId] as const;

export function useAdminGateways() {
  return useQuery<Gateway[], ApiError>({
    queryKey: gatewaysKey,
    queryFn: async () => {
      const result = await fetchJSON<{ data: Gateway[] | null }>(apiUrl("/admin/gateways"));
      return result.data ?? [];
    },
  });
}

export function useAdminGateway(gatewayId: string) {
  return useQuery<GatewayDetail, ApiError>({
    queryKey: gatewayKey(gatewayId),
    enabled: Boolean(gatewayId),
    queryFn: () => fetchJSON<GatewayDetail>(apiUrl(`/admin/gateways/${encodeURIComponent(gatewayId)}`)),
  });
}

export function useCreateAdminGateway() {
  const qc = useQueryClient();
  return useMutation<Enrollment, ApiError, GatewayInput>({
    mutationFn: (body) => fetchJSON<Enrollment>(apiUrl("/admin/gateways"), { method: "POST", body: JSON.stringify(body) }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: gatewaysKey }),
  });
}

type GatewayAction = "disable" | "drain" | "reenable" | "resume";
export function useAdminGatewayAction() {
  const qc = useQueryClient();
  return useMutation<void, ApiError, { gatewayId: string; action: GatewayAction }>({
    mutationFn: ({ gatewayId, action }) => fetchJSON<void>(apiUrl(`/admin/gateways/${encodeURIComponent(gatewayId)}:${action}`), { method: "POST" }),
    onSuccess: (_result, { gatewayId }) => {
      void qc.invalidateQueries({ queryKey: gatewaysKey });
      void qc.invalidateQueries({ queryKey: gatewayKey(gatewayId) });
    },
  });
}

export function useAdminGatewayEnrollment(action: "reenroll" | "replace-enrollment-token") {
  const qc = useQueryClient();
  return useMutation<Enrollment, ApiError, string>({
    mutationFn: (gatewayId) => fetchJSON<Enrollment>(apiUrl(`/admin/gateways/${encodeURIComponent(gatewayId)}:${action}`), { method: "POST" }),
    onSuccess: (_result, gatewayId) => {
      void qc.invalidateQueries({ queryKey: gatewaysKey });
      void qc.invalidateQueries({ queryKey: gatewayKey(gatewayId) });
    },
  });
}

export function useDeleteAdminGateway() {
  const qc = useQueryClient();
  return useMutation<void, ApiError, string>({
    mutationFn: (gatewayId) => fetchJSON<void>(apiUrl(`/admin/gateways/${encodeURIComponent(gatewayId)}`), { method: "DELETE", body: JSON.stringify({ consequencesAcknowledged: true }) }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: gatewaysKey }),
  });
}
