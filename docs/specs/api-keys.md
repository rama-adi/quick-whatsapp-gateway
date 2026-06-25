# API Keys

Status: stub — filled in with the implementation.

Scope: account-global API keys with {read,send,manage,events} permissions, argon2id hashing, prefixes, rotation, expiry, last-used tracking.

## Crypto primitives (implemented)

Key generation/verification lives in `internal/crypto` (see `http-foundation.md`):

- `GenerateAPIKey() (fullKey, prefix, hash string, err error)` — `fullKey="wak_<rand>"`
  shown once; store `prefix` (`"wak_xxxx"`, the deterministic 4-char head) and `hash`
  (argon2id PHC). The full key is never stored.
- `PrefixOf(presented) (string, error)` then `store.APIKeyRepo.GetByPrefix(prefix)` then
  `VerifyAPIKey(presented, row.KeyHash) bool` is the auth hot path.

The auth middleware consumes an `APIKeyVerifier{ Verify(ctx, rawKey) (*domain.APIKey,
*domain.Tenant, error) }` (see `internal/http/middleware`). The api-key **service**
(later stage) implements that interface: parse prefix → fetch row → verify hash → check
revoked/expired → return key+tenant; it also bumps `last_used_at`.
