# HTTP foundation & router

Status: implemented (R1).

Shared transport plumbing and the chi router. The gateway is a **pure WhatsApp engine with no
human login**: no `/auth` surface, no embedded SPA, no cookie middleware. Every `/api/v1` route is
gated by the two-acceptor authz middleware (JWKS-verified JWT **or** better-auth api-key); health/
metrics probes stay open. Masterplan §4.3, §4.4, §13.

## Router (`internal/http/router.go`)

`NewRouter(RouterConfig)` builds the chi tree:

- **Base stack:** `Recover` (outermost) → `RequestID` → `Logger` → `CORS(FRONTEND_ORIGINS)` (only
  when origins are configured).
- **Open probes:** `GET /healthz`, `GET /readyz`, `/metrics` (Prometheus) — unauthenticated.
- **`/api/v1`** — an authenticated group wraps `authz.Authenticate(Tokens, Keys)` plus an optional
  edge `RateLimit`; each resource group then composes its capability gate. `GET /events` (NDJSON
  stream) sits behind `RequireEvents`. The OpenAPI spec is optionally served raw at
  `/api/v1/openapi.yaml`.
- **No SPA, no `/auth`:** any unmatched path is a JSON `404` via `WriteError(ErrNotFound)`.

`RouterConfig` carries `Tokens authz.TokenVerifier`, `Keys authz.KeyVerifier`, `CORSOrigins`,
`Limiter`, `Readiness`, `OpenAPIPath`, `Log`.

### Route surface (capability per group)

| Group | Gate | Routes |
|---|---|---|
| Webhooks | `RequireManage` | `/webhooks`, `/webhooks/{id}` (POST/GET/PATCH/DELETE) |
| Admin | `RequireSuperAdmin` | `GET /admin/sessions`, `POST /admin/sessions/{session}:backfill`, `GET /admin/sessions/{session}/backfill` |
| Sessions | `RequireManage` | `/sessions`, `:start`/`:stop`/`:restart`/`:logout`, `/me`, `/qr`, `/pairing-code`, `POST`/`GET /sessions/{session}/backfill` (crypt15 backup import) |
| Messages | `RequireSend` | `/sessions/{session}/messages` (+ edit/revoke/reaction/forward/vote) |
| Chats/Contacts/Groups/Channels | `RequireRead` (GET) / `RequireSend` (mutations) | per-session sub-resources |
| Status/Presence | `RequireSend` | `/status`, `/presence` |
| Events | `RequireEvents` | `GET /events` (NDJSON, §11) |

> **Removed vs v1:** `/auth/*` (→ better-auth on the frontend), `/keys*` (→ better-auth api-key
> plugin — the gateway only *verifies* keys), `/auth/admin/*` (→ better-auth admin plugin), and the
> embedded SPA static handler.

## Authz middleware (`internal/authz`)

Not in `internal/http/middleware` — auth lives in `internal/authz` and is detailed in
[`trust-model.md`](trust-model.md). `Authenticate(tokens, keys)` is the single two-acceptor
middleware: a `Bearer` that parses as a JWT → `TokenVerifier` (JWKS); otherwise the bearer /
`x-api-key` → `KeyVerifier` (shared `apikey` table); neither → `401`. It puts the resolved
`Principal` on the context; `RequireRead/Send/Manage/Events/SuperAdmin` (`gates.go`) authorize.
`CORS(origins)` (`cors.go`) allows the configured `FRONTEND_ORIGINS` with `Authorization` for the
browser → gateway calls (stream + actions). There is **no Authula cookie bridge** — that whole v1
path is gone.

## `internal/http/middleware`

Transport-only middleware (no auth):

```go
func Recover(log *slog.Logger) func(http.Handler) http.Handler   // panic -> logged 500 JSON; outermost
func RequestID() func(http.Handler) http.Handler                 // honor inbound X-Request-Id else mint; ctx + echo
func Logger(log *slog.Logger) func(http.Handler) http.Handler    // one slog line: method,path,status,dur,reqid,org

type RateLimiter interface { Allow(ctx, key string) (bool, error) }
func SessionOrOrganizationKey(r *http.Request) string  // "session:<id>" if :session present, else "org:<id>", else "anon"
func RateLimit(limiter RateLimiter, keyFn RateLimitKeyFunc) func(http.Handler) http.Handler
```

Rate-limit key choice: session routes carry `:session` so they limit **per WhatsApp number**;
others fall back to an **org-wide** bucket (`org:<id>`, renamed from v1's `tenant:`). **Fail-open**
on a limiter backend error; a clean `(false, nil)` → `429`.

## `internal/httpx`

```go
// Context (org-keyed, not tenant):
func OrganizationID(ctx) string            ; func SetOrganizationID(ctx, id) context.Context
func APIKeyCtx(ctx) *domain.APIKey         ; func SetAPIKey(ctx, *domain.APIKey) context.Context
func PrincipalValue(ctx) any               ; func SetPrincipalValue(ctx, any) context.Context
func RequestID(ctx) string                 ; func SetRequestID(ctx, id) context.Context

// §11 error envelope {"error":{code,message,details}}:
func WriteJSON(w, status int, v any)
func WriteError(w, err error)              // *domain.APIError -> mapped status; else masked 500
// not_found 404, unauthorized 401, forbidden 403, validation_error 400, rate_limited 429,
// conflict 409, not_implemented 501, internal 500.

// Decode (1 MiB cap, unknown-field reject -> validation_error):
func DecodeJSON[T any](r, dst *T) error    ; func DecodeJSONLimit[T any](r, dst *T, max int64) error

// Pagination (?limit=&cursor=, opaque; envelope {"data":[...],"nextCursor":...}):
func ParsePage(r) (limit int, cursor string)  ; func ListEnvelope[T any](w, items []T, nextCursor string)
```

## `internal/crypto`

AES-256-GCM (random 12-byte nonce prepended) for webhook HMAC secrets / sensitive config at rest
(`APP_ENCRYPTION_KEY`, masterplan §11):

```go
func NewAESGCM(base64Key string) (*AESGCM, error)
func (*AESGCM) Encrypt(plaintext []byte) ([]byte, error)
func (*AESGCM) Decrypt(ciphertext []byte) ([]byte, error)  // ErrMalformedCiphertext on tamper/short/wrong-key
```

> The v1 custom api-key helpers (`GenerateAPIKey`/`VerifyAPIKey`/argon2id, `wak_` prefix) are
> obsolete in v2 — key minting/verification moved to better-auth + `internal/authz`
> ([`api-keys.md`](api-keys.md)). Any residual helpers in `internal/crypto/apikey.go` are dead and
> a cleanup candidate (see Verify notes).

## Tests

- crypto: AES round-trip, nonce randomization, tamper/short/wrong-key detection, bad-key reject.
- httpx: every error-code→status mapping + non-APIError masking + wrapped unwrap; decode
  OK/unknown-field/malformed/too-large; pagination clamp/default/bad + list envelope (nil→`[]`);
  ctx getters.
- middleware: recover 500 JSON; requestID generate + inbound propagation; ratelimit
  allow/deny/fail-open + key-by-session/org.
- authz + router auth: covered in [`trust-model.md`](trust-model.md).

Run: `CGO_ENABLED=0 go test ./internal/http/... ./internal/httpx/... ./internal/crypto/...`.
