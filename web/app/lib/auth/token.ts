// Gateway-JWT minting (§4.1, §4.7) — the bridge between the better-auth session
// (cookie, on the frontend domain) and the short-lived Bearer the BROWSER sends
// to the gateway.
//
// The browser holds the better-auth session cookie but NOT a gateway JWT. It
// calls this server function (which runs with the cookie) to mint/refresh a
// 5-min EdDSA JWT carrying {sub, activeOrganizationId, orgRole, role}. The
// gateway verifies it locally via JWKS — no frontend round-trip on the hot path.
//
// Refresh model (§4.7): there is no refresh token; the session IS the long-lived
// credential. To refresh, the client just calls this again (the token provider
// in ~/lib/api/token-provider.ts does this ~every 5 min and reconnects the stream).

import { createServerFn } from "@tanstack/react-start";
import { getRequest } from "@tanstack/react-start/server";

/**
 * Mint a fresh gateway JWT from the current better-auth session. Returns null if
 * the caller has no valid session (so the client can redirect to /login). The
 * token endpoint stops minting once the session is revoked — that's the §4.7
 * "revocable refresh token" behavior.
 */
export const mintGatewayToken = createServerFn({ method: "GET" }).handler(
  async (): Promise<{ token: string } | null> => {
    const { auth } = await import("./server");
    const request = getRequest();
    try {
      const result = await auth.api.getToken({ headers: request.headers });
      if (!result?.token) return null;
      return { token: result.token };
    } catch {
      // No session / unauthenticated → no token.
      return null;
    }
  },
);
