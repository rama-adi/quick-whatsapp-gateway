# whatsmeow Gateway

Run a WhatsApp account as an HTTP API. You pair a phone once from a web dashboard, then send
messages, read chats and contacts, manage groups, and receive events over a stream or webhooks —
all over JSON with a Bearer token. Built on [whatsmeow](https://github.com/tulir/whatsmeow);
multi-tenant, self-hostable, free.

> **Legal / ban risk:** This drives WhatsApp through an unofficial client. WhatsApp prohibits
> bots and unofficial clients; automated use may break its Terms and get your number **banned**.
> Built-in rate limiting and human-mimicry lower the risk but do not remove it. **Use at your own
> risk.**

## Quick start

Infra (MySQL + Redis) runs in Docker; both apps run on the host for a fast edit loop.

You need: Go 1.26+, Node 22+ with pnpm (`corepack enable`), Docker, and
[`air`](https://github.com/air-verse/air). No C compiler — the keystore uses a pure-Go SQLite
driver, so everything builds with `CGO_ENABLED=0`.

```sh
cp deploy/.env.example .env     # then set APP_ENCRYPTION_KEY, BETTER_AUTH_SECRET, …
make infra-up                   # start MySQL + Redis (bound to localhost)
make migrate                    # create the WhatsApp data tables
cd web && pnpm install && pnpm drizzle-kit migrate && cd ..   # create the auth tables

make dev      # terminal 1: gateway on :8080 (hot reload)
make web      # terminal 2: frontend on :3000 (HMR)
```

Then, in the browser:

1. Open **http://localhost:3000** and sign up. Your account gets its own organization; everything
   you create belongs to it.
2. Create a session and scan the QR code with WhatsApp on your phone
   (**Settings → Linked devices → Link a device**).
3. Once it shows **connected**, send a test message from the dashboard.

`make infra-reset` wipes the dev database when you want a clean slate.

## Common tasks

The dashboard is for humans. For scripts and integrations, mint an **API key** in the dashboard
(**Settings → API keys**) and call the gateway directly. Keys start with `wa_` and go in the
`Authorization` header. The API base is `http://localhost:8080/api/v1`; `{session}` is the session
id from the dashboard.

**Send a message**

```sh
curl -X POST http://localhost:8080/api/v1/sessions/{session}/messages \
  -H "Authorization: Bearer wa_your_api_key" \
  -H "Content-Type: application/json" \
  -d '{"type":"text","to":"6281234567890@s.whatsapp.net","text":"Hello from the gateway"}'
```

`type` also accepts `poll`, `location`, and `contact`. Add `?async` to enqueue instead of sending
inline, or an `Idempotency-Key` header to make retries safe.

**Subscribe to the event stream**

A long-lived NDJSON stream — one JSON event per line — for incoming messages, delivery and read
receipts, and connection changes. The key needs the `events` permission.

```sh
curl -N http://localhost:8080/api/v1/events \
  -H "Authorization: Bearer wa_your_api_key"
# add ?types=message,message.status   to filter (incoming messages + receipts)
# add ?session={session}              to watch one session
```

**Register a webhook**

Have the gateway POST events to your URL instead of (or as well as) the stream. Omit `sessionId`
to receive from every session in your organization.

```sh
curl -X POST http://localhost:8080/api/v1/webhooks \
  -H "Authorization: Bearer wa_your_api_key" \
  -H "Content-Type: application/json" \
  -d '{
    "url": "https://example.com/wa-hook",
    "events": ["message", "message.status"],
    "secret": "shared-signing-secret"
  }'
```

Every endpoint — messages, chats, contacts, groups, channels, presence — is documented at
**http://localhost:3000/docs** once the frontend is running, with task guides under `/docs/guides`
and the full API reference under `/docs/api`.

## Architecture

Two services that deploy independently. The dashboard mints identity; the gateway only verifies it
and does the WhatsApp work, so the two never have to sit on the same machine.

```
            Bearer token (better-auth JWT or wa_ api-key)
  Browser ───────────────────────────────────────────►  Gateway (Go, whatsmeow)
     │  ▲                                                   │   ├─ REST API     /api/v1
     │  │ session cookie + mint JWT (/api/auth/token)       │   ├─ event stream /events
     ▼  │                                                   │   └─ webhooks (out)
  Frontend (TanStack Start + better-auth)                   │
     ├─ /api/auth/* (JWKS, token, admin, api-keys, orgs)    │  keystore → SQLite (volume)
     └─ direct read-only Drizzle reads ──┐                  │
                                         ▼                  ▼
                              MySQL  (auth tables ⇄ WhatsApp data tables)
                                         ▲                  │
   revoke a key / ban a user PUBLISH ──► └── Redis ─────────┘  queue / rate-limit / stream
```

| Service | Job | Stack |
| --- | --- | --- |
| **Gateway** (Go) | Holds the live WhatsApp sessions; serves the REST API, event stream, and webhooks. No human login — it verifies the frontend's JWTs (against the cached JWKS) and `wa_` api-keys locally, with no per-request callback. | whatsmeow, `CGO_ENABLED=0` |
| **Frontend** (`web/`) | The dashboard and the only place humans log in. Owns identity: email/password, 2FA, api-keys, organizations. Reads WhatsApp data straight from MySQL for fast pages; acts through the gateway API. | TanStack Start, better-auth, Drizzle |

Resources are owned by an organization, not a user, so sharing a connection means inviting someone
into your org. Revoking a key or banning a user in the dashboard reaches every gateway within about
a minute over a Redis control channel.

## Deploying

Two images, built and shipped on their own:

```sh
make build                                                      # gateway (deploy/Dockerfile)
docker build -f deploy/Dockerfile.web -t whatsmeow-frontend .   # frontend
```

- `deploy/docker-compose.yml` — everything in one place (both services + MySQL + Redis).
- `deploy/docker-compose.external.yml` — bring your own MySQL + Redis.
- `deploy/docker-compose.dev.yml` — infra only, for the host dev loop above.

The frontend runs anywhere it can reach MySQL and the gateway. The gateway runs near WhatsApp with
its `/data/keystore` volume. Gateway config is set through env vars (`HTTP_ADDR`, `MYSQL_DSN`,
`WHATSMEOW_STORE_DSN`, `BETTER_AUTH_URL`, `FRONTEND_ORIGINS`, `APP_ENCRYPTION_KEY`, …); the full
list with defaults lives in `deploy/.env.example`.

## Repo layout

```
cmd/server/    gateway entrypoint (also: server migrate up|down)
internal/      gateway: authz/ · controlbus/ · http/ · wa/ · store/ · webhooks/ · stream/ · queue/
migrations/    WhatsApp data tables (golang-migrate)
web/           frontend: TanStack Start + better-auth + Drizzle + shadcn; docs site under /docs
deploy/        Dockerfiles · compose files · .env.example
docs/          openapi.yaml (the API contract) · specs/*.md (per-subsystem design)
```

## For contributors

Start with the design spec in [`masterplan-mvp.md`](./masterplan-mvp.md), then the per-subsystem
specs in [`docs/specs/`](./docs/specs/) — [`_V2-STATUS.md`](./docs/specs/_V2-STATUS.md) maps them
out. The API contract of record is [`docs/openapi.yaml`](./docs/openapi.yaml).

Keep both halves green:

```sh
go build ./... && go test ./...                    # gateway
cd web && pnpm build && pnpm typecheck && pnpm test # frontend
```

`scripts/smoke.sh` drives the trust seam end to end against a running stack — register a user, mint
a JWT, call the gateway with a Bearer token, create a session, fetch its pairing QR — and fails
loudly on any unexpected status. The pair → send → stream steps need a real phone and print as
manual instructions at the end.

```sh
BETTER_AUTH_URL=http://localhost:3000 GATEWAY_URL=http://localhost:8080 scripts/smoke.sh
```

The v1 single-binary version (embedded auth, React SPA, MySQL keystore) is preserved at git tag
`mvp-v1`.
