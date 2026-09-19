// Browser-side gateway-JWT provider (§4.1, §4.7).
//
// The browser talks to the gateway DIRECTLY with a Bearer JWT. That JWT is
// short-lived (5 min) and minted from the better-auth session via the
// mintGatewayToken server function. This module caches the current token and
// refreshes it before expiry so gateway actions and realtime ticket minting use
// a valid Bearer without hammering the token endpoint.
//
// Refresh policy (§4.7): refresh proactively a bit before the 5-min TTL; on a
// 401 from the gateway, force a refresh once. Identity owners clear the token
// on sign-out or org switch; the realtime owner observes that invalidation and
// replaces the organization-scoped socket.

import { mintGatewayToken } from "~/lib/auth/token";

// Refresh ~30s before the 5-min TTL so in-flight calls never carry a dead token.
const REFRESH_BEFORE_MS = 30_000;
const TTL_MS = 5 * 60_000;

let current: string | null = null;
let expiresAt = 0;
let inflight: Promise<string | null> | null = null;
let generation = 0;

type RefreshListener = (token: string | null) => void;
const listeners = new Set<RefreshListener>();

/** Subscribe to token refreshes (the stream uses this to reconnect). */
export function onTokenRefresh(fn: RefreshListener): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

function emit(token: string | null): void {
  for (const fn of listeners) fn(token);
}

async function refresh(): Promise<string | null> {
  if (inflight) return inflight;
  const refreshGeneration = generation;
  const request = (async () => {
    const res = await mintGatewayToken();
    if (refreshGeneration !== generation) return null;
    current = res?.token ?? null;
    expiresAt = current ? Date.now() + TTL_MS : 0;
    emit(current);
    return current;
  })();
  inflight = request;
  try {
    return await request;
  } finally {
    if (inflight === request) inflight = null;
  }
}

/**
 * Return a valid gateway JWT, refreshing if missing/near-expiry. `force` skips
 * the cache (used after a gateway 401).
 */
export async function getGatewayToken(force = false): Promise<string | null> {
  const stale = Date.now() >= expiresAt - REFRESH_BEFORE_MS;
  if (force || !current || stale) {
    return refresh();
  }
  return current;
}

/** Drop the cached token (e.g. on sign-out). */
export function clearGatewayToken(): void {
  generation += 1;
  current = null;
  expiresAt = 0;
  inflight = null;
  emit(null);
}
