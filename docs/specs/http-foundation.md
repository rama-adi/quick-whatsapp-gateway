# HTTP foundation

Status: implemented (R1; central-router Increment A; **Increment 9: gateway HTTP API removed**).

> **Increment 9.** The gateway serves **no HTTP API at all**: no chi router, no
> huma operations, no admission gate, no OpenAPI, no assertion middleware. Its
> entire network surface is the private mTLS engine gRPC listener plus a minimal
> `net/http` probe server (`/healthz`, `/readyz`, `/metrics`). The former
> `backend/internal/http` chi router (`NewRouter`, `RouterConfig`, `AdmissionGate`) is
> deleted; `backend/internal/http/handlers` (huma operations) and
> `backend/internal/http/middleware` remain because the **API** mounts them on its own
> public surface.

The API is the single front door: it owns authn/CORS/control-bus, the Huma REST
surface + generated OpenAPI (`docs/openapi.yaml`, served at
`/api/v1/openapi.yaml`), ticketed WebSocket realtime, and the `public.v1` gRPC
adapters in `backend/internal/apigrpc` on `API_PUBLIC_GRPC_ADDR`. Public REST and gRPC
share one authn stack, org scoping, and error semantics — see
[`router.md`](router.md), [`grpc-contracts.md`](grpc-contracts.md),
[`trust-model.md`](trust-model.md).

## Gateway probes (`backend/cmd/gateway/main.go`)

The composition root builds its own tiny handler instead of any framework:

- `GET /healthz` — always `200 ok`.
- `GET /readyz` — `200 ready` only when the acknowledged control-stream
  heartbeat reports READY/RUN **and** journal capacity is not critical. There is
  no MySQL or Redis ping: those dependencies no longer exist on the gateway.
- `/metrics` — Prometheus collector registry.

## API route surface (capability per group)

The API mounts the shared huma operations from `backend/internal/http/handlers`:

| Group | Gate | Routes |
|---|---|---|
| Webhooks | `RequireManage` | `/webhooks`, `/webhooks/{id}` (POST/GET/PATCH/DELETE) |
| Admin | `RequireSuperAdmin` | `GET /admin/sessions`, `POST /admin/sessions/{session}:backfill`, `GET /admin/sessions/{session}/backfill` |
| Gateway admin | `RequireSuperAdmin` | gateway inventory/enrollment/lifecycle administration |
| Sessions | `RequireManage` | `/sessions`, `:start`/`:stop`/`:restart`/`:logout`, `/me`, `/qr`, `/pairing-code`, `POST`/`GET /sessions/{session}/backfill` (crypt15 backup import) |
| Messages | `RequireSend` | `/sessions/{session}/messages` (+ edit/revoke/reaction/forward/vote) |
| Chats/Contacts/Groups/Channels | `RequireRead` (GET) / `RequireSend` (mutations) | per-session sub-resources |
| Status/Presence | `RequireSend` | `/status`, `/presence` |

> **Removed vs v1:** `/auth/*` (→ better-auth on the frontend), `/keys*` (→ better-auth api-key
> plugin; the API verifies keys), `/auth/admin/*` (→ better-auth admin plugin), and the
> embedded SPA static handler.

## Authz middleware (`backend/internal/authz`)

Auth lives in `backend/internal/authz` and is detailed in [`trust-model.md`](trust-model.md):
the two-acceptor end-user middleware (JWT via JWKS / api-key via the shared
`apikey` table) runs **only on the API**. `RequireRead/Send/Manage/Events/SuperAdmin`
(`gates.go`) authorize from the authenticated principal.
There is **no Authula cookie bridge** — that whole v1 path is gone.

## `backend/internal/http/middleware`

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
service hop. Every event includes service, method, normalized route pattern,
status, duration in milliseconds, request ID, and organization. Raw request
paths are omitted because path parameters can contain JIDs, phone numbers,
message IDs, or other resource identifiers. A 5xx event is emitted at error
level. For `503`, the originating seam records a stable `failure_cause`
(`deadline_exceeded`, `context_canceled`, `upstream_timeout`, or
`gateway_unavailable`) and `failure_source`. The API's log events include the
numeric `database/sql` pool pressure and, on session routes, the
non-identifying WhatsApp status snapshot resolved through the engine facade.
Bodies, headers, tokens, JIDs, phone numbers, and message text are never logged.

`X-Request-Id` is validated or minted once at the API, echoed to the caller,
stored in context, and forwarded to internal hops so log events share one
correlation ID even when the caller supplied none. Prometheus exports
`gateway_request_failures_total{service,source,cause}` and the standard Go SQL
pool collector; labels remain low-cardinality and never contain request,
session, user, or organization IDs.

Rate-limit key choice: session routes carry `:session` so they limit **per WhatsApp number**;
others fall back to an **org-wide** bucket (`org:<id>`). **Fail-open**
on a limiter backend error; a clean `(false, nil)` → `429`.

## `backend/internal/httpx`

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
// (gateway_unavailable: a session's owning gateway is missing/unreachable/unfresh.)

// Decode (1 MiB cap, unknown-field reject -> validation_error):
func DecodeJSON[T any](r, dst *T) error    ; func DecodeJSONLimit[T any](r, dst *T, max int64) error

// Pagination (?limit=&cursor=, opaque; envelope {"data":[...],"nextCursor":...}):
func ParsePage(r) (limit int, cursor string)  ; func ListEnvelope[T any](w, items []T, nextCursor string)
```

The gateway binary uses only `WriteError` for its `/readyz` failure body.

## `backend/internal/crypto`

AES-256-GCM (random 12-byte nonce prepended) for webhook HMAC secrets / sensitive config at rest
(`APP_ENCRYPTION_KEY`, masterplan §11):

```go
func NewAESGCM(base64Key string) (*AESGCM, error)
func (*AESGCM) Encrypt(plaintext []byte) ([]byte, error)
func (*AESGCM) Decrypt(ciphertext []byte) ([]byte, error)  // ErrMalformedCiphertext on tamper/short/wrong-key
```

## Tests

- crypto: AES round-trip, nonce randomization, tamper/short/wrong-key detection, bad-key reject.
- httpx: every error-code→status mapping + non-APIError masking + wrapped unwrap; decode
  OK/unknown-field/malformed/too-large; pagination clamp/default/bad + list envelope (nil→`[]`);
  ctx getters.
- middleware: recover 500 JSON; requestID generate + inbound propagation; ratelimit
  allow/deny/fail-open + key-by-session/org.
- authz: covered in [`trust-model.md`](trust-model.md).

Run: `CGO_ENABLED=0 go -C backend test ./internal/http/... ./internal/httpx/... ./internal/crypto/...`.
