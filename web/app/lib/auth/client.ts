// better-auth REACT client (browser) — replaces the v1 Authula client.
//
// Talks to THIS frontend's better-auth API at /api/auth/* (same origin, cookie
// session). Plugin clients mirror the server plugins so the typed methods exist:
//   - twoFactorClient   -> authClient.twoFactor.*
//   - adminClient        -> authClient.admin.* (list/ban/impersonate/setRole)
//   - apiKeyClient       -> authClient.apiKey.* (create/list/delete, org-scoped)
//   - organizationClient -> authClient.organization.* (create/switch/invite/members)
//   - jwtClient          -> authClient.token() helper for the gateway JWT
//
// Note: getting the gateway JWT for browser->gateway calls goes through the
// token PROVIDER in ~/lib/auth/token.ts (which calls /api/auth/token); this
// client is for auth UI flows (login, 2FA, org switch, key management).

import { createAuthClient } from "better-auth/react";
import {
  adminClient,
  jwtClient,
  organizationClient,
  twoFactorClient,
} from "better-auth/client/plugins";
import { apiKeyClient } from "@better-auth/api-key/client";

export const authClient = createAuthClient({
  // Same-origin: better-auth is mounted at /api/auth on this app. Leaving
  // baseURL unset lets the client default to the current origin's /api/auth.
  plugins: [
    twoFactorClient(),
    adminClient(),
    apiKeyClient(),
    organizationClient(),
    jwtClient(),
  ],
});

// Convenience re-exports matching common call sites (Stage 3 wires the forms).
export const {
  signIn,
  signUp,
  signOut,
  useSession,
  getSession,
  twoFactor,
  admin,
  apiKey,
  organization,
} = authClient;

export type AuthClient = typeof authClient;
