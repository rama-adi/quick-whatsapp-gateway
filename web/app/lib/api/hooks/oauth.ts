// OAuth-application ("Sign in with WhatsApp" provider) management hooks.
// Milestone 4 (oauth.md §6.2). Talks to the org-scoped huma CRUD under
// /api/v1/oauth-apps via the same Bearer-JWT gateway client every other resource
// uses (browser -> router, §4). Shapes come straight from the generated OpenAPI
// schema — no hand-typed models.
//
// Pattern mirrors the FROZEN foundation hooks (sessions.ts / webhooks.ts): a qk.*
// key per resource, cursor lists via listPageFetcher/nextCursor, mutations that
// invalidate the affected keys. Kept in its own surface file (not the frozen
// _shared/types) so the OAuth surface owns its own contract.

import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
  type InfiniteData,
  type UseInfiniteQueryResult,
  type UseMutationResult,
  type UseQueryResult,
} from "@tanstack/react-query";
import { qk } from "../../query";
import type { components } from "../schema";
import type { ApiError, Page } from "../envelope";
import { apiUrl, fetchJSON, listPageFetcher, nextCursor } from "./_shared";

// --- Types (from the generated schema — single source of truth) -------------

export type OAuthApp = components["schemas"]["OAuthApp"];
export type OAuthAppWithSecret = components["schemas"]["OAuthAppWithSecret"];
export type OAuthGrant = components["schemas"]["OAuthGrant"];
export type OAuthAppBody = components["schemas"]["OauthAppBody"];
export type OAuthAppPatchBody = components["schemas"]["OauthAppPatchBody"];

export type OAuthClientType = NonNullable<OAuthApp["clientType"]>;
export type OAuthMode = "dm" | "group";
export type OAuthAcr = NonNullable<OAuthGrant["lastAcr"]>;

// --- Queries ----------------------------------------------------------------

export function useOAuthApps(): UseInfiniteQueryResult<
  InfiniteData<Page<OAuthApp>, string | undefined>,
  ApiError
> {
  return useInfiniteQuery({
    queryKey: qk.oauthApps(),
    initialPageParam: undefined as string | undefined,
    queryFn: listPageFetcher<OAuthApp>("/oauth-apps"),
    getNextPageParam: nextCursor,
  });
}

export function useOAuthApp(id: string): UseQueryResult<OAuthApp, ApiError> {
  return useQuery({
    queryKey: qk.oauthApp(id),
    enabled: Boolean(id),
    queryFn: () => fetchJSON<OAuthApp>(apiUrl(`/oauth-apps/${encodeURIComponent(id)}`)),
  });
}

export function useOAuthAppGrants(
  id: string,
): UseInfiniteQueryResult<
  InfiniteData<Page<OAuthGrant>, string | undefined>,
  ApiError
> {
  return useInfiniteQuery({
    queryKey: qk.oauthAppGrants(id),
    enabled: Boolean(id),
    initialPageParam: undefined as string | undefined,
    queryFn: listPageFetcher<OAuthGrant>(
      `/oauth-apps/${encodeURIComponent(id)}/grants`,
    ),
    getNextPageParam: nextCursor,
  });
}

// --- Mutations --------------------------------------------------------------

/** Create returns the OAuthAppWithSecret — `clientSecret` is present exactly once
 * here (confidential clients only). The caller must surface it immediately. */
export function useCreateOAuthApp(): UseMutationResult<
  OAuthAppWithSecret,
  ApiError,
  OAuthAppBody
> {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (body) =>
      fetchJSON<OAuthAppWithSecret>(apiUrl("/oauth-apps"), {
        method: "POST",
        body: JSON.stringify(body),
      }),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: qk.oauthApps() });
    },
  });
}

export function useUpdateOAuthApp(
  id: string,
): UseMutationResult<OAuthApp, ApiError, OAuthAppPatchBody> {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (body) =>
      fetchJSON<OAuthApp>(apiUrl(`/oauth-apps/${encodeURIComponent(id)}`), {
        method: "PATCH",
        body: JSON.stringify(body),
      }),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: qk.oauthApp(id) });
      void client.invalidateQueries({ queryKey: qk.oauthApps() });
    },
  });
}

export function useDeleteOAuthApp(): UseMutationResult<void, ApiError, string> {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (id) =>
      fetchJSON<void>(apiUrl(`/oauth-apps/${encodeURIComponent(id)}`), {
        method: "DELETE",
      }),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: qk.oauthApps() });
    },
  });
}

/** Rotate returns a fresh OAuthAppWithSecret with `clientSecret` shown once; the
 * old secret is invalid immediately. */
export function useRotateOAuthAppSecret(
  id: string,
): UseMutationResult<OAuthAppWithSecret, ApiError, void> {
  const client = useQueryClient();
  return useMutation({
    mutationFn: () =>
      fetchJSON<OAuthAppWithSecret>(
        apiUrl(`/oauth-apps/${encodeURIComponent(id)}:rotate-secret`),
        { method: "POST" },
      ),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: qk.oauthApp(id) });
    },
  });
}

export function useSetOAuthAppStatus(
  id: string,
): UseMutationResult<void, ApiError, "enable" | "disable"> {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (action) =>
      fetchJSON<void>(
        apiUrl(`/oauth-apps/${encodeURIComponent(id)}:${action}`),
        { method: "POST" },
      ),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: qk.oauthApp(id) });
      void client.invalidateQueries({ queryKey: qk.oauthApps() });
    },
  });
}

export function useRevokeOAuthAppGrant(
  appId: string,
): UseMutationResult<void, ApiError, string> {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (grantId) =>
      fetchJSON<void>(
        apiUrl(
          `/oauth-apps/${encodeURIComponent(appId)}/grants/${encodeURIComponent(grantId)}`,
        ),
        { method: "DELETE" },
      ),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: qk.oauthAppGrants(appId) });
    },
  });
}
