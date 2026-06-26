// Authula Admin operations (tenants list/ban/impersonate) under /auth/admin/*.
// FROZEN — owned by the foundation agent.
//
// These hit the Authula Admin plugin (recon §9), NOT the /api/v1 surface, and
// use the cookie session + CSRF header via authFetch.

import {
  useMutation,
  useQuery,
  useQueryClient,
  type UseMutationResult,
  type UseQueryResult,
} from "@tanstack/react-query";
import { authFetch } from "./client";
import { qk } from "../query";
import type { ApiError } from "../api/envelope";

/** A tenant as returned by the Authula admin user listing (shape is loose). */
export interface Tenant {
  id: string;
  email?: string;
  name?: string;
  banned?: boolean;
  createdAt?: string | number;
  [k: string]: unknown;
}

interface AdminUserList {
  data?: Tenant[];
  users?: Tenant[];
}

export function useTenants(): UseQueryResult<Tenant[], ApiError> {
  return useQuery({
    queryKey: qk.tenants(),
    queryFn: async () => {
      const res = await authFetch<AdminUserList>("/admin/users");
      return res.data ?? res.users ?? [];
    },
  });
}

export function useBanTenant(): UseMutationResult<
  void,
  ApiError,
  { id: string; ban: boolean }
> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, ban }) =>
      authFetch<void>(`/admin/users/${encodeURIComponent(id)}/${ban ? "ban" : "unban"}`, {
        method: "POST",
      }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.tenants() });
    },
  });
}

export function useImpersonate(): UseMutationResult<
  unknown,
  ApiError,
  { userId: string }
> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ userId }) =>
      authFetch("/admin/impersonations", {
        method: "POST",
        body: JSON.stringify({ userId }),
      }),
    onSuccess: () => {
      // Identity changed: blow away every cache so surfaces refetch as the target.
      void qc.invalidateQueries();
    },
  });
}
