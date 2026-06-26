// Session resource hooks (list/get/create/lifecycle/QR/pairing).
// FROZEN — owned by the foundation agent. sessionId is always the first arg.

import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
  type UseInfiniteQueryResult,
  type UseMutationResult,
  type UseQueryResult,
} from "@tanstack/react-query";
import { qk } from "../../query";
import type { ApiError, Page } from "../envelope";
import type {
  CreateSessionRequest,
  QRCode,
  SessionAction,
  SessionMe,
  WASession,
} from "../types";
import { apiUrl, fetchJSON, listPageFetcher, nextCursor } from "./_shared";

export function useSessions(): UseInfiniteQueryResult<
  { pages: Page<WASession>[]; pageParams: (string | undefined)[] },
  ApiError
> {
  return useInfiniteQuery({
    queryKey: qk.sessions(),
    initialPageParam: undefined as string | undefined,
    queryFn: listPageFetcher<WASession>("/sessions"),
    getNextPageParam: nextCursor,
  });
}

export function useSession(s: string): UseQueryResult<WASession, ApiError> {
  return useQuery({
    queryKey: qk.session(s),
    enabled: Boolean(s),
    queryFn: () => fetchJSON<WASession>(apiUrl(`/sessions/${encodeURIComponent(s)}`)),
  });
}

export function useSessionMe(s: string): UseQueryResult<SessionMe, ApiError> {
  return useQuery({
    queryKey: qk.sessionMe(s),
    enabled: Boolean(s),
    queryFn: () => fetchJSON<SessionMe>(apiUrl(`/sessions/${encodeURIComponent(s)}/me`)),
  });
}

export function useSessionQR(s: string): UseQueryResult<QRCode, ApiError> {
  // The live value arrives via the auth.qr event; this seeds the initial value.
  return useQuery({
    queryKey: qk.sessionQR(s),
    enabled: Boolean(s),
    queryFn: () => fetchJSON<QRCode>(apiUrl(`/sessions/${encodeURIComponent(s)}/qr`)),
    staleTime: Infinity, // events keep it fresh; don't auto-refetch
  });
}

export function useCreateSession(): UseMutationResult<
  WASession,
  ApiError,
  CreateSessionRequest
> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body) =>
      fetchJSON<WASession>(apiUrl("/sessions"), {
        method: "POST",
        body: JSON.stringify(body),
      }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.sessions() });
    },
  });
}

export function useDeleteSession(): UseMutationResult<void, ApiError, { sessionId: string }> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ sessionId }) =>
      fetchJSON<void>(apiUrl(`/sessions/${encodeURIComponent(sessionId)}`), {
        method: "DELETE",
      }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.sessions() });
    },
  });
}

/** start/stop/restart/logout via the POST /sessions/{id}:{action} endpoints. */
export function useSessionLifecycle(): UseMutationResult<
  WASession,
  ApiError,
  { sessionId: string; action: SessionAction }
> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ sessionId, action }) =>
      fetchJSON<WASession>(
        apiUrl(`/sessions/${encodeURIComponent(sessionId)}:${action}`),
        { method: "POST" },
      ),
    onSuccess: (data, { sessionId }) => {
      qc.setQueryData(qk.session(sessionId), data);
      void qc.invalidateQueries({ queryKey: qk.sessions() });
    },
  });
}

export function usePairingCode(): UseMutationResult<
  { code: string },
  ApiError,
  { sessionId: string; phone: string }
> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ sessionId, phone }) =>
      fetchJSON<{ code: string }>(
        apiUrl(`/sessions/${encodeURIComponent(sessionId)}/pairing-code`),
        { method: "POST", body: JSON.stringify({ phone }) },
      ),
    onSuccess: (data, { sessionId }) => {
      qc.setQueryData(qk.sessionPairing(sessionId), data);
    },
  });
}
