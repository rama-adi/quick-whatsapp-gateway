# HTTP foundation & router

Status: implemented (R1; central-router Increment A).

Shared transport plumbing and the chi router. The gateway is a **pure WhatsApp engine with no
human login**: no `/auth` surface, no embedded SPA, no cookie middleware.

> **Central-router (Increment A).** The gateway **no longer authenticates end users** and **no longer
> mounts CORS or serves the public OpenAPI spec** — browsers hit the **router**, which owns the
> public route surface + CORS and serves `/api/v1/openapi.yaml` ([`router.md`](router.md)). Every
> `/api/v1` route is now gated by the **internal-assertion middleware** (`assertion.Middleware`,
> wired via `RouterConfig.Auth`): it verifies the router's request-bound Ed25519 assertion and
> rebuilds the `Principal`. The capability gates (`RequireRead/Send/Manage/Events/SuperAdmin`) and
> org-scoped store queries are **unchanged** — they read the asserted principal. Health/metrics
> probes stay open. Masterplan §4.3, §4.4, §13; [`trust-model.md`](trust-model.md).

## Router (`internal/http/router.go`)

`NewRouter(RouterConfig)` builds the chi tree:

- **Base stack:** `Recover` (outermost) → `RequestID` → `Logger`. **CORS is no longer mounted on the
  gateway** (central-router) — it lives on the router.
- **Open probes:** `GET /healthz`, `GET /readyz`, `/metrics` (Prometheus) — unauthenticated.
- **`/api/v1`** — an authenticated group wraps the **assertion middleware** (`RouterConfig.Auth`,
  `assertion.Middleware`) plus an optional edge `RateLimit`; each resource group then composes its
  capability gate. The gateway **no longer exposes `/events`** — realtime is WebSocket-only on the
  router (`GET /api/v1/realtime`); the gateway only publishes events to Redis for the router to
  fan out.
- **No SPA, no `/auth`:** any unmatched path is a JSON `404` via `WriteError(ErrNotFound)`.

`RouterConfig` carries `Auth func(http.Handler) http.Handler` (the assertion middleware),
`Limiter`, `Readiness`, `Log`. **Dropped vs R1 (central-router):** `Tokens`/`Keys` (end-user
verifiers — now on the router), `CORSOrigins` (CORS now on the router), and serving the OpenAPI spec
(`OpenAPIPath` — the router serves `/api/v1/openapi.yaml`, plan D9).

### Route surface (capability per group)

| Group | Gate | Routes |
|---|---|---|
| Webhooks | `RequireManage` | `/webhooks`, `/webhooks/{id}` (POST/GET/PATCH/DELETE) |
| Admin | `RequireSuperAdmin` | `GET /admin/sessions`, `POST /admin/sessions/{session}:backfill`, `GET /admin/sessions/{session}/backfill` |
| Sessions | `RequireManage` | `/sessions`, `:start`/`:stop`/`:restart`/`:logout`, `/me`, `/qr`, `/pairing-code`, `POST`/`GET /sessions/{session}/backfill` (crypt15 backup import) |
| Messages | `RequireSend` | `/sessions/{session}/messages` (+ edit/revoke/reaction/forward/vote) |
| Chats/Contacts/Groups/Channels | `RequireRead` (GET) / `RequireSend` (mutations) | per-session sub-resources |
| Status/Presence | `RequireSend` | `/status`, `/presence` |

> **Removed vs v1:** `/auth/*` (→ better-auth on the frontend), `/keys*` (→ better-auth api-key
> plugin — the gateway only *verifies* keys), `/auth/admin/*` (→ better-auth admin plugin), and the
> embedded SPA static handler.

## Authz middleware (`internal/authz`)

Auth lives in `internal/authz` and is detailed in [`trust-model.md`](trust-model.md).

> **Central-router (Increment A).** `authz.Authenticate(tokens, keys)` — the two-acceptor end-user
> middleware (JWT via JWKS / api-key via the shared `apikey` table) — and `CORS(FRONTEND_ORIGINS)`
> now run **on the router**, not the gateway. On the gateway the `/api/v1` group instead runs the
> **internal-assertion middleware** (`assertion.Middleware`, `internal/assertion`): it verifies the
> router's request-bound Ed25519 assertion, rebuilds the `Principal`, and puts it on the context.
> `RequireRead/Send/Manage/Events/SuperAdmin` (`gates.go`) authorize from that principal, unchanged.
> There is **no Authula cookie bridge** — that whole v1 path is gone.
> Browser preflights receive a fixed allow-list of public headers; caller-requested headers are not
> reflected, so internal-only headers cannot be opted into the browser CORS policy.

## `internal/http/middleware`

Transport-only middleware (no auth):

```go
func Recover(log *slog.Logger) func(http.Handler) http.Handler   // panic -> logged 500 JSON; outermost
func RequestID() func(http.Handler) http.Handler                 // honor bounded visible-ASCII X-Request-Id else mint; ctx + echo + forward
func Logger(log *slog.Logger, ...LoggerOptions) func(http.Handler) http.Handler // one canonical request event

type RateLimiter interface { Allow(ctx, key string) (bool, error) }
func SessionOrOrganizationKey(r *http.Request) string  // "session:<id>" if :session present, else "org:<id>", else "anon"
func RateLimit(limiter RateLimiter, keyFn RateLimitKeyFunc) func(http.Handler) http.Handler
```

The canonical `http_request` event is the single completion record for each
service hop. Every event includes service, method, concrete path, route pattern,
status, duration in milliseconds, request ID, and organization. A 5xx event is
emitted at error level. For `503`, the originating seam records a stable
`failure_cause` (`deadline_exceeded`, `context_canceled`, `upstream_timeout`, or
`gateway_unavailable`) and `failure_source` (`gateway_handler`, `router_resolve`,
`router_registry`, `router_proxy`, or `gateway_response`). Gateway events also
include numeric `database/sql` pool pressure and, on session routes, the
non-identifying WhatsApp status/connected/logged-in snapshot. Bodies, headers,
tokens, JIDs, phone numbers, and message text are never logged.

`X-Request-Id` is validated or minted once at the router, echoed to the caller,
stored in context, and forwarded to the gateway. Router and gateway log events
therefore share one correlation ID even when the caller supplied none. The
Prometheus endpoint exports `gateway_request_failures_total{service,source,cause}`
and the standard Go SQL pool collector; labels remain low-cardinality and never
contain request, session, user, or organization IDs.

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
// conflict 409, gateway_unavailable 503, not_implemented 501, internal 500.
// (gateway_unavailable: a session's owning gateway is missing/not active/stale — the router
//  returns it for a stranded session; central-router Increment A.)

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
