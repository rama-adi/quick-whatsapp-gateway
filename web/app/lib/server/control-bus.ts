// Control-bus publisher (server-only, PUBLISH-ONLY) — R4 trust seam (§4.6).
//
// The frontend's ONLY Redis dependency: it publishes low-volume `ctrl:*`
// revocation messages to PUBSUB_REDIS_URL. Every gateway subscribes to `ctrl:*`
// and reacts instantly (evict api-key cache, drop live NDJSON streams, add to a
// short JWT deny-list). The frontend NEVER touches the work Redis (REDIS_URL).
//
// Channels & payloads (load-bearing — the gateway matches these exactly):
//   ctrl:apikey.revoked  { keyId, ts }
//   ctrl:user.banned     { userId, ts }
//   ctrl:member.removed  { userId, organizationId, ts }
//
// If PUBSUB_REDIS_URL is unset we NO-OP with a one-time warning — the gateway's
// ~60s positive-cache TTL is the backstop (§4.6), so a missed publish degrades
// to "revoked within the TTL window" rather than failing the mutation.

import Redis from "ioredis";

let _pub: Redis | null | undefined;
let _warned = false;

function getPublisher(): Redis | null {
  if (_pub !== undefined) return _pub;
  const url = process.env.PUBSUB_REDIS_URL;
  if (!url) {
    if (!_warned) {
      console.warn(
        "[control-bus] PUBSUB_REDIS_URL unset — revocations will not be published; " +
          "gateways fall back to their ~60s api-key cache TTL backstop (§4.6).",
      );
      _warned = true;
    }
    _pub = null;
    return null;
  }
  // lazyConnect: the socket opens on the first publish, not at import.
  _pub = new Redis(url, {
    lazyConnect: true,
    maxRetriesPerRequest: 1,
    enableOfflineQueue: false,
  });
  _pub.on("error", (err) => {
    console.error("[control-bus] redis error:", err?.message ?? err);
  });
  return _pub;
}

async function publish(channel: string, payload: Record<string, unknown>): Promise<void> {
  const pub = getPublisher();
  if (!pub) return;
  try {
    if (pub.status === "wait" || pub.status === "close" || pub.status === "end") {
      await pub.connect();
    }
    await pub.publish(channel, JSON.stringify({ ...payload, ts: Date.now() }));
  } catch (err) {
    // Fire-and-forget semantics (§4.6): a failed publish is covered by the
    // gateway's cache-TTL backstop. Never let it fail the auth mutation.
    console.error(`[control-bus] publish to ${channel} failed:`, err);
  }
}

/** Published on api-key revoke — gateways evict the key + drop its streams. */
export function publishApiKeyRevoked(keyId: string): Promise<void> {
  return publish("ctrl:apikey.revoked", { keyId });
}

/** Published on user ban — gateways drop all the user's keys/streams + deny-list. */
export function publishUserBanned(userId: string): Promise<void> {
  return publish("ctrl:user.banned", { userId });
}

/** Published on org member removal — gateways drop (userId, orgId) access. */
export function publishMemberRemoved(
  userId: string,
  organizationId: string,
): Promise<void> {
  return publish("ctrl:member.removed", { userId, organizationId });
}
