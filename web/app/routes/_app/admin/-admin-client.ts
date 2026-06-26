// Admin-surface local hooks backed by the better-auth ADMIN + ORGANIZATION
// clients (replaces the v1 Authula `~/lib/auth/admin` module, which no longer
// exists in v2). Colocated under the admin route dir; the `-` prefix keeps
// TanStack Start from treating it as a route.
//
// These call authClient.admin.* / authClient.organization.* (see
// app/lib/auth/client.ts). The better-auth client returns `{ data, error }`;
// we unwrap into TanStack Query so the v1 page components keep their
// query/mutation ergonomics. Identity-changing actions (ban/impersonate/role)
// also fan out a control-bus publish on the SERVER via better-auth hooks — the
// frontend client just calls the endpoint; the gateway reacts to Redis (R4).

import {
  useMutation,
  useQuery,
  useQueryClient,
  type UseMutationResult,
  type UseQueryResult,
} from "@tanstack/react-query";
import { authClient } from "~/lib/auth/client";
import { qk } from "~/lib/query";

// The frozen query-key factory (~/lib/query) has `tenants()` but no `orgs()`
// key. Rather than edit the frozen file, the org list keys off a surface-local
// constant. NOTE for Verify: hoist an `qk.orgs()` into the factory if the
// org→cache bridge ever needs to target this key.
const ORGS_KEY = ["admin", "orgs"] as const;

/** A platform user ("tenant") as returned by better-auth admin listUsers. */
export interface Tenant {
  id: string;
  email?: string;
  name?: string;
  banned?: boolean | null;
  role?: string | null;
  createdAt?: string | number | Date;
  [k: string]: unknown;
}

/** An organization row as returned by the org client's listOrganizations. */
export interface Org {
  id: string;
  name: string;
  slug?: string | null;
  createdAt?: string | number | Date;
  [k: string]: unknown;
}

/** Narrow a better-auth `{ data, error }` result, throwing the error message. */
async function unwrap<T>(p: Promise<{ data: T | null; error: { message?: string } | null }>): Promise<T> {
  const { data, error } = await p;
  if (error) throw new Error(error.message ?? "Request failed");
  return data as T;
}

/** List all platform users across orgs (admin plugin). */
export function useTenants(): UseQueryResult<Tenant[], Error> {
  return useQuery({
    queryKey: qk.tenants(),
    queryFn: async () => {
      const res = await unwrap(
        authClient.admin.listUsers({ query: { limit: 200 } }),
      );
      // better-auth types `users` as UserWithRole[]; relax to the loose Tenant
      // shape (extra fields tolerated) via unknown.
      const users = (res as unknown as { users?: Tenant[] }).users ?? [];
      return users;
    },
  });
}

/** Ban / unban a platform user. */
export function useBanTenant(): UseMutationResult<unknown, Error, { id: string; ban: boolean }> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, ban }) =>
      ban
        ? unwrap(authClient.admin.banUser({ userId: id }))
        : unwrap(authClient.admin.unbanUser({ userId: id })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.tenants() });
    },
  });
}

/** Set a platform role on a user (e.g. super_admin / user). */
export function useSetRole(): UseMutationResult<unknown, Error, { id: string; role: string }> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, role }) =>
      // The admin client types `role` against the declared adminRoles union
      // ("user"|"admin" by default); the platform uses "super_admin", so cast.
      unwrap(authClient.admin.setRole({ userId: id, role: role as "user" })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.tenants() });
    },
  });
}

/** Impersonate a user; the session cookie is swapped server-side. */
export function useImpersonate(): UseMutationResult<unknown, Error, { userId: string }> {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ userId }) => unwrap(authClient.admin.impersonateUser({ userId })),
    onSuccess: () => {
      // Identity changed: blow away every cache so surfaces refetch as the target.
      void qc.invalidateQueries();
    },
  });
}

/** List every organization (org plugin). */
export function useOrgs(): UseQueryResult<Org[], Error> {
  return useQuery({
    queryKey: ORGS_KEY,
    queryFn: async () => {
      const res = await unwrap(authClient.organization.list());
      return (res as Org[]) ?? [];
    },
  });
}
