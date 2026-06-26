// Typed fetch wrapper for the gateway JSON API under {GATEWAY_URL}/api/v1 (R4).
//
// v2 shape (§4, §12): the BROWSER talks to the gateway DIRECTLY — a separate
// origin (VITE_GATEWAY_URL) — with a `Authorization: Bearer <jwt>`. The frontend
// server does NOT proxy gateway traffic. So:
//   - no credentials:"include" (the gateway is cross-origin; auth is the Bearer).
//   - the JWT comes from the token provider (mints/refreshes the 5-min token).
//   - on a 401 we force one token refresh and retry, covering a just-expired JWT.
//
// Errors are normalized into the shared ApiError; 204/empty -> undefined.

import { ApiError, parseError } from "./envelope";
import { getGatewayToken } from "./token-provider";

// The gateway origin. Vite inlines VITE_GATEWAY_URL at build for the client
// bundle; trailing slashes are trimmed so apiUrl() can join cleanly.
const GATEWAY_URL = (import.meta.env.VITE_GATEWAY_URL ?? "").replace(/\/+$/, "");
const API_BASE = `${GATEWAY_URL}/api/v1`;

/** Build a fully-qualified gateway API URL. Pass a path starting with "/". */
export function apiUrl(path: string): string {
  return `${API_BASE}${path}`;
}

/**
 * Perform a JSON request against the gateway. Resolves to the parsed body typed
 * as T (or undefined for 204/no-content). Throws ApiError on any non-2xx.
 * Attaches the gateway Bearer JWT and retries once on 401 with a fresh token.
 */
export async function fetchJSON<T>(input: string, init?: RequestInit): Promise<T> {
  return doFetch<T>(input, init, false);
}

async function doFetch<T>(
  input: string,
  init: RequestInit | undefined,
  retried: boolean,
): Promise<T> {
  const headers = new Headers(init?.headers);
  if (init?.body !== undefined && init.body !== null && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }
  if (!headers.has("Accept")) headers.set("Accept", "application/json");

  const token = await getGatewayToken(retried);
  if (token) headers.set("Authorization", `Bearer ${token}`);

  const res = await fetch(input, {
    ...init,
    headers,
    // No cookies cross-origin: the Bearer JWT is the credential (§4).
  });

  // A 401 likely means the short-lived JWT just expired — refresh once + retry.
  if (res.status === 401 && !retried) {
    return doFetch<T>(input, init, true);
  }

  if (res.status === 204 || res.status === 205) {
    return undefined as T;
  }

  const text = await res.text();
  const parsed: unknown = text ? safeJsonParse(text) : undefined;

  if (!res.ok) {
    throw parseError(parsed, res.status);
  }

  return parsed as T;
}

function safeJsonParse(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return undefined;
  }
}

export { ApiError };
