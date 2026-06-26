# API keys

Status: implemented (R1). Live-validated against better-auth 1.6.22.

There are **no custom gateway keys** in v2. Programmatic access uses better-auth's **api-key
plugin**; the gateway only **verifies** presented keys against the shared `apikey` table. The v1
custom Go keys (argon2id, `wak_` prefix, `internal/crypto` + an `api_keys` table) are gone.
Masterplan §4.2. The trust seam as a whole is documented in [`trust-model.md`](trust-model.md).

## Lifecycle (frontend, better-auth)

The frontend's better-auth `apiKey` plugin (`web/app/lib/auth/server.ts`) owns create / list /
revoke / rotate, with permissions, expiry, and rate limits, surfaced in the dashboard
(`web/app/routes/_app/user/keys.tsx`). Keys are **org-scoped**: the plugin is configured
`references: "organization"`, so a created key's `reference_id` is the active organization and
`organizationId` is required on create. Key prefix: `wa_`.

The gateway never mints, lists, or deletes keys — `/keys*` routes do **not** exist on the gateway.

## Verification (gateway)

`internal/authz` validates a presented key locally — no callback to the frontend — so the gateway
stays self-sufficient if the frontend is down. `APIKeyVerifier.VerifyKey`
(`internal/authz/apikey.go`):

1. Hash the raw key with better-auth's default scheme (`authz.DefaultHasher`).
2. `keyRepo.GetByHash(hash)` against the shared `apikey` table (`store.APIKeyRepo`, read-only).
3. Reject if `!enabled`, if `expires_at` has passed, or if there is no owning org.
4. Return an org-scoped `Principal{Kind: api-key, OrganizationID, KeyID, KeyPermissions}` —
   **no** `UserID`.

### Live-confirmed `apikey` schema (better-auth 1.6.22)

| Column | Used as |
|---|---|
| `key` | the hash = **`base64url(SHA-256(rawKey))` unpadded** (`base64.RawURLEncoding` of `sha256.Sum256`) — not hex, not padded |
| `reference_id` | the owning **organization id** (`references: "organization"`) → `Principal.OrganizationID` |
| `permissions` | resource→actions map, e.g. `{"gateway":["read","send","manage","events"]}` → `KeyPermissions` |
| `enabled` | disabled → reject |
| `expires_at` / `created_at` | `TIMESTAMP(3)`; expiry enforced against `now()` |

The presented `Authorization: Bearer <key>` or `x-api-key` header is accepted by the auth
middleware (`authz.Authenticate`) as the api-key acceptor when it does not parse as a JWT.

## Caching & revocation

Validated keys sit in a per-gateway positive cache (`internal/authz/apikey_cache.go`, ~60 s TTL,
fail-closed). Revocation is **instant** via the Redis control bus: the frontend publishes
`ctrl:apikey.revoked {keyId}` (or `ctrl:user.banned`) on revoke/ban; gateways evict the cache
entry and drop any live NDJSON streams the key authenticated. The TTL is the backstop for a missed
message; boot reconcile prunes stale keys. Full detail: [`trust-model.md`](trust-model.md) §
control bus.

## Hash replicability — the pinned-version risk

Local validation depends on better-auth's hash being deterministic and replicable in Go. The
`Hasher` interface exists so the scheme can be swapped per pinned version. The **R5 contract
test** creates a key in better-auth and validates it in the gateway, locking the assumption.
**Fallback** (`internal/authz/apikey_remote.go`, `RemoteKeyVerifier`): call
`POST {BETTER_AUTH_URL}/api/auth/api-key/verify` behind a short-TTL cache — used only if a future
better-auth version makes the hash non-replicable. Masterplan §19 #1.

## How it's tested

`apikey_test.go` — hash correctness (SHA-256 → unpadded base64url), enabled/expired/no-org
rejection, org-scoped principal shape, lookup-error → 401 mapping; `apikey_cache_test.go` — TTL
expiry, evict-by-keyId/userId. Trust-seam contract test (R5) is the cross-service safety net.

Run: `CGO_ENABLED=0 go test ./internal/authz/...`.
