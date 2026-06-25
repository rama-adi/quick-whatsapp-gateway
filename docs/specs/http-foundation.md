# HTTP foundation primitives

Status: implemented (Phase 3, stage "HTTP foundation").

Shared, handler-agnostic transport plumbing. No router or REST handlers live here —
this is the toolkit every handler/middleware composes. Three packages:

- `internal/crypto` — symmetric encryption (AES-GCM) + API-key generate/verify.
- `internal/httpx` — JSON encode, §11 error envelope, body decode, pagination,
  request-scoped context keys.
- `internal/http/middleware` — Recover, RequestID, Logger, API-key auth +
  permission gates, rate limit, Authula cookie bridge.

These signatures are the contract for later stages (services + handlers). Match them
exactly.

## internal/crypto

AES-256-GCM, random 12-byte nonce **prepended** to ciphertext (`nonce||sealed`).
Used for webhook HMAC secrets and sensitive config at rest (masterplan §10).

```go
func NewAESGCM(base64Key string) (*AESGCM, error)   // base64 std of 32 bytes (APP_ENCRYPTION_KEY)
func NewAESGCMFromKey(key []byte) (*AESGCM, error)   // raw 32 bytes
func (*AESGCM) Encrypt(plaintext []byte) ([]byte, error)
func (*AESGCM) Decrypt(ciphertext []byte) ([]byte, error)  // ErrMalformedCiphertext on short/tamper/wrong-key

var ErrInvalidKey, ErrMalformedCiphertext error
```

API keys — `wak_` prefix, argon2id (golang.org/x/crypto/argon2), PHC-format hash:

```go
const APIKeyPrefix = "wak_"
func GenerateAPIKey() (fullKey, prefix, hash string, err error) // full="wak_<rand>", prefix="wak_xxxx" (4 chars), hash=argon2id PHC
func PrefixOf(fullKey string) (string, error)                   // parse lookup prefix from presented key (ErrInvalidAPIKey)
func VerifyAPIKey(fullKey, hash string) bool                    // constant-time; false on any malformed hash/mismatch
```

The `prefix` is a deterministic head of `fullKey` (`"wak_"` + first 4 random chars), so
the auth hot path does `PrefixOf(presented)` → `APIKeyRepo.GetByPrefix` →
`VerifyAPIKey(presented, row.KeyHash)`. The full key is shown to the user **once** at
creation; only `prefix` and `hash` are stored.

argon2id params: time=1, memory=64 MiB, threads=4, keyLen=32, salt=16 bytes.

## internal/httpx

Context keys (read side; setters used by middleware):

```go
func TenantID(ctx) string            ;  func SetTenantID(ctx, id) context.Context
func APIKeyCtx(ctx) *domain.APIKey   ;  func SetAPIKey(ctx, *domain.APIKey) context.Context
func RequestID(ctx) string           ;  func SetRequestID(ctx, id) context.Context
```

JSON + error envelope (§11 `{"error":{code,message,details}}`):

```go
func WriteJSON(w, status int, v any)
func WriteError(w, err error)  // *domain.APIError -> mapped status; any other error -> generic 500 (masked)
```

Code→status map: not_found 404, unauthorized 401, forbidden 403, validation_error 400,
rate_limited 429, conflict 409, not_implemented 501, internal 500, unknown→500.
`WriteError` uses `errors.As`, so wrapped APIErrors map correctly.

Decode (body-size limit + unknown-field rejection; failures → `validation_error`):

```go
const DefaultMaxBodyBytes int64 = 1 << 20  // 1 MiB
func DecodeJSON[T any](r *http.Request, dst *T) error
func DecodeJSONLimit[T any](r *http.Request, dst *T, maxBytes int64) error
```

Pagination (§11 `?limit=&cursor=`, opaque cursor; envelope `{"data":[...],"nextCursor":...}`):

```go
const DefaultLimit = 50; MaxLimit = 200; MinLimit = 1
func ParsePage(r *http.Request) (limit int, cursor string)  // limit clamped; cursor verbatim
func ListEnvelope[T any](w, items []T, nextCursor string)   // nil items -> []; empty cursor omitted
```

Repos return `store.Page[T]{Items, NextCursor}` → handler calls
`ListEnvelope(w, page.Items, page.NextCursor)`.

## internal/http/middleware

```go
const RequestIDHeader = "X-Request-Id"

func Recover(log *slog.Logger) func(http.Handler) http.Handler   // panic -> logged 500 JSON; outermost
func RequestID() func(http.Handler) http.Handler                 // honor inbound X-Request-Id else mint ULID; ctx + echo header
func Logger(log *slog.Logger) func(http.Handler) http.Handler    // 1 slog line: method,path,status,dur,reqid,tenant
```

API-key auth — consumer interface (implemented by the api-key service later):

```go
type APIKeyVerifier interface {
    Verify(ctx, rawKey string) (*domain.APIKey, *domain.Tenant, error)
}
func APIKeyAuth(verifier APIKeyVerifier) func(http.Handler) http.Handler
// "Authorization: Bearer <key>" (case-insensitive scheme). On success sets
// httpx.SetAPIKey + httpx.SetTenantID. Missing/malformed header or any verifier
// error/nil result -> 401 (never leaks detail).
```

Permission gates (read the API key on the ctx; 401 if no key, 403 if missing perm):

```go
func RequireRead()   func(http.Handler) http.Handler  // domain.Permissions.Read
func RequireSend()   func(http.Handler) http.Handler  // .Send
func RequireManage() func(http.Handler) http.Handler  // .Manage
func RequireEvents() func(http.Handler) http.Handler  // .Events
```

Rate limit — consumer interface (implemented by `outbound` redis limiter):

```go
type RateLimiter interface { Allow(ctx, key string) (bool, error) }
type RateLimitKeyFunc func(r *http.Request) string
func SessionOrTenantKey(r *http.Request) string  // "session:<:session>" if chi param present, else "tenant:<id>", else "anon"
func RateLimit(limiter RateLimiter, keyFn RateLimitKeyFunc) func(http.Handler) http.Handler  // nil keyFn -> SessionOrTenantKey
```

**Key choice:** session routes always carry the `:session` path param, so they are
limited **per WhatsApp number**; non-session routes fall back to a **tenant-wide**
bucket. **Fail-open:** a limiter backend error allows the request (a Redis outage
degrades to no limiting, not a full outage). A clean `(false, nil)` -> 429.

Cookie bridge (dashboard) — composes Authula's optional cookie auth:

```go
func CookieSession(
    optionalAuth func(http.Handler) http.Handler,   // Auth.OptionalCookieAuth()
    tenantFrom   func(*http.Request) (string, bool), // Auth.CurrentTenantID
) func(http.Handler) http.Handler
// Runs optionalAuth, then lifts the resolved tenant into ctx via httpx.SetTenantID.
// NEVER rejects — enrichment only.

func RequireTenant() func(http.Handler) http.Handler  // 401 if ctx tenant == ""
```

**"Either API key OR cookie"** routes: chain both enrichers (`APIKeyAuth` is reject-on-
fail, so for optional-either use a non-rejecting api-key variant or place `CookieSession`
+ `RequireTenant` and gate once). For pure-dashboard routes use `CookieSession` +
`RequireTenant`; for pure-API routes use `APIKeyAuth` + a `Require*` perm gate.

## Tests

- crypto: AES round-trip, nonce-randomization, tamper detection, short ciphertext,
  wrong-key failure, bad-key rejection; api-key generate/verify, prefix format,
  PrefixOf round-trip + malformed reject, wrong-key reject, malformed-hash reject,
  uniqueness.
- httpx: every error-code→status mapping + non-APIError masking + wrapped unwrap;
  decode OK/unknown-field/malformed/empty/wrong-type/trailing-data/too-large;
  pagination parse (clamp/default/bad) + list envelope (incl. nil→[]); ctx getters.
- middleware: recover 500 JSON; requestID generate + inbound propagation; apikey
  valid/missing/malformed/case-insensitive/verifier-reject; perm gate allow/deny + no-key
  401; ratelimit allow/deny/fail-open + key-by-session/tenant; cookie sets tenant +
  never-rejects; RequireTenant.
```
