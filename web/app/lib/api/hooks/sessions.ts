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
  type UseQueryOptions,
} from "@tanstack/react-query";
import { qk } from "../../query";
import type { ApiError, Page } from "../envelope";
import type {
  CreateSessionRequest,
  PasskeyStep,
  QRCode,
  SessionAction,
  SessionFailureReason,
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

export function useSession(
  s: string,
  refetchInterval?: UseQueryOptions<WASession, ApiError>["refetchInterval"],
): UseQueryResult<WASession, ApiError> {
  return useQuery({
    queryKey: qk.session(s),
    refetchInterval,
    enabled: Boolean(s),
    queryFn: ({ signal }) =>
      fetchJSON<WASession>(apiUrl(`/sessions/${encodeURIComponent(s)}`), { signal }),
  });
}

export function useSessionMe(s: string): UseQueryResult<SessionMe, ApiError> {
  return useQuery({
    queryKey: qk.sessionMe(s),
    enabled: Boolean(s),
    queryFn: ({ signal }) =>
      fetchJSON<SessionMe>(apiUrl(`/sessions/${encodeURIComponent(s)}/me`), { signal }),
  });
}

export function useSessionQR(
  s: string,
  enabled = true,
): UseQueryResult<QRCode, ApiError> {
  // The live value arrives via the auth.qr event; this seeds the initial value.
  return useQuery({
    queryKey: qk.sessionQR(s),
    enabled: Boolean(s) && enabled,
    queryFn: ({ signal }) =>
      fetchJSON<QRCode>(apiUrl(`/sessions/${encodeURIComponent(s)}/qr`), { signal }),
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
      // Events can advance the cache before the action response arrives.
      qc.setQueryData<WASession>(qk.session(sessionId), (current) => current ?? data);
      void qc.invalidateQueries({ queryKey: qk.sessions() });
    },
  });
}

/**
 * The pending passkey step of a pairing, if WhatsApp asked for one. It exists
 * only in the cache: auth.passkey events write it and any status other than
 * scan_qr_code clears it (cacheBridge).
 */
export function useSessionPasskey(s: string): PasskeyStep | undefined {
  const { data } = useQuery<PasskeyStep | null>({
    queryKey: qk.sessionPasskey(s),
    queryFn: () => null,
    enabled: false,
    staleTime: Infinity,
  });
  return data ?? undefined;
}

/**
 * The phone pairing code of the current pairing attempt. It exists only in the
 * cache: usePairingCode's onSuccess and auth.code events write it, and any
 * status other than scan_qr_code clears it (cacheBridge). Read the code here,
 * never from the mutation result, so a newer event or a clear always wins.
 */
export function useSessionPairingCode(s: string): { code: string } | undefined {
  const { data } = useQuery<{ code: string } | null>({
    queryKey: qk.sessionPairing(s),
    queryFn: () => null,
    enabled: false,
    staleTime: Infinity,
  });
  return data ?? undefined;
}

/** The failureReason carried by the latest failed session.status event. */
export function useSessionFailure(s: string): SessionFailureReason | undefined {
  const { data } = useQuery<SessionFailureReason | null>({
    queryKey: qk.sessionFailure(s),
    queryFn: () => null,
    enabled: false,
    staleTime: Infinity,
  });
  return data ?? undefined;
}

/** POST /sessions/{id}/passkey: forward the WebAuthn credential JSON. */
export function useSubmitPasskey(): UseMutationResult<
  { accepted: boolean },
  ApiError,
  { sessionId: string; response: unknown }
> {
  return useMutation({
    mutationFn: ({ sessionId, response }) =>
      fetchJSON<{ accepted: boolean }>(
        apiUrl(`/sessions/${encodeURIComponent(sessionId)}/passkey`),
        { method: "POST", body: JSON.stringify({ response }) },
      ),
  });
}

/** POST /sessions/{id}/passkey/confirm: the codes on screen and phone match. */
export function useConfirmPasskey(): UseMutationResult<
  { accepted: boolean },
  ApiError,
  { sessionId: string }
> {
  return useMutation({
    mutationFn: ({ sessionId }) =>
      fetchJSON<{ accepted: boolean }>(
        apiUrl(`/sessions/${encodeURIComponent(sessionId)}/passkey/confirm`),
        { method: "POST" },
      ),
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
