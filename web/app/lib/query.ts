// TanStack Query client + the query-key factory (qk).
// FROZEN — owned by the foundation agent. Surface agents import qk + queryClient,
// never edit this file. A new key is a request to the foundation agent.

import { QueryClient, QueryCache, MutationCache } from "@tanstack/react-query";
import { isApiError } from "./api/envelope";
import type { ContactFilter } from "./api/types";

/** Where to send the browser when a request comes back 401 (unauthenticated). */
const LOGIN_PATH = "/login";

function redirectToLogin(): void {
  if (typeof window === "undefined") return;
  const { pathname, search } = window.location;
  // Avoid a redirect loop if we're already on an auth page.
  if (pathname.startsWith("/login") || pathname.startsWith("/register") || pathname.startsWith("/2fa")) {
    return;
  }
  const next = encodeURIComponent(pathname + search);
  window.location.assign(`${LOGIN_PATH}?next=${next}`);
}

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 30_000,
      gcTime: 5 * 60_000,
      refetchOnWindowFocus: false,
      retry: (failureCount, error) => {
        // Never retry deterministic 4xx; allow a couple of retries otherwise.
        if (isApiError(error) && error.status && error.status >= 400 && error.status < 500) {
          return false;
        }
        return failureCount < 2;
      },
    },
    mutations: {
      retry: false,
    },
  },
  queryCache: new QueryCache({
    onError: (error) => {
      if (isApiError(error) && error.isUnauthorized) {
        redirectToLogin();
      }
    },
  }),
  mutationCache: new MutationCache({
    onError: (error) => {
      if (isApiError(error) && error.isUnauthorized) {
        redirectToLogin();
      }
    },
  }),
});

/**
 * Canonical query-key factory. Every hook keys off these so the event→cache
 * bridge (cacheBridge.ts) can target exactly the same keys for live updates.
 */
export const qk = {
  me: () => ["me"] as const,

  sessions: () => ["sessions"] as const,
  session: (s: string) => ["sessions", s] as const,
  sessionQR: (s: string) => ["sessions", s, "qr"] as const,
  sessionPairing: (s: string) => ["sessions", s, "pairing"] as const,
  sessionMe: (s: string) => ["sessions", s, "me"] as const,

  adminSessions: () => ["admin", "sessions"] as const,

  chats: (s: string) => ["sessions", s, "chats"] as const,
  chat: (s: string, c: string) => ["sessions", s, "chats", c] as const,
  chatMessages: (s: string, c: string) =>
    ["sessions", s, "chats", c, "messages"] as const,

  contacts: (s: string, f: ContactFilter) =>
    ["sessions", s, "contacts", f] as const,
  contact: (s: string, lid: string) => ["sessions", s, "contacts", lid] as const,

  groups: (s: string) => ["sessions", s, "groups"] as const,
  group: (s: string, gid: string) => ["sessions", s, "groups", gid] as const,

  presence: (s: string, jid: string) => ["presence", s, jid] as const,

  keys: () => ["keys"] as const,
  webhooks: () => ["webhooks"] as const,

  tenants: () => ["tenants"] as const,

  oauthApps: () => ["oauth-apps"] as const,
  oauthApp: (id: string) => ["oauth-apps", id] as const,
  oauthAppGrants: (id: string) => ["oauth-apps", id, "grants"] as const,
} as const;

export type { ContactFilter };
