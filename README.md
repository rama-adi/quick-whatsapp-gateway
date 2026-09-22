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
make api      # terminal 2: API HTTP :8090 + public gRPC :8081
make web      # terminal 3: frontend on :3000 (HMR)
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
(**Settings → API keys**) and call the API. Keys start with `wa_` and go in the
`Authorization` header. The API base is `http://localhost:8090/api/v1`; `{session}` is the session
id from the dashboard.

**Send a message**

```sh
curl -X POST http://localhost:8090/api/v1/sessions/{session}/messages \
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
curl -N http://localhost:8090/api/v1/events \
  -H "Authorization: Bearer wa_your_api_key"
# add ?types=message,message.status   to filter (incoming messages + receipts)
# add ?session={session}              to watch one session
```

**Register a webhook**

Have the gateway POST events to your URL instead of (or as well as) the stream. Omit `sessionId`
to receive from every session in your organization.

```sh
curl -X POST http://localhost:8090/api/v1/webhooks \
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

Three services deploy independently. The frontend mints identity, the API is the
only public front door, and the gateway holds the live WhatsApp sessions.

```
Browser / clients ──► API :8090 HTTP or :8081 gRPC ──► Gateway :8080 (private)
         │                    │                              │
         ▼                    ▼                              ▼
Frontend :3000           MySQL + Redis                SQLite keystore
```

| Service | Job | Stack |
| --- | --- | --- |
| **API** (Go) | The public front door on HTTP `8090` and gRPC `8081`. Authenticates callers, owns WA schema migration, and brokers current gateway requests. | `CGO_ENABLED=0` |
| **Gateway** (Go) | Holds live WhatsApp sessions behind private HTTP `8080`. It trusts only the API assertion, not end-user credentials. | whatsmeow, `CGO_ENABLED=0` |
| **Frontend** (`web/`) | Dashboard and identity service; calls the API for actions. | TanStack Start, better-auth, Drizzle |

Resources are owned by an organization, not a user, so sharing a connection means inviting someone
into your org. Revoking a key or banning a user in the dashboard reaches the API immediately
over a Redis control channel, with cache TTLs as a backstop.

## Deploying

Three app images, built and shipped on their own:

```sh
docker build -f deploy/Dockerfile.api -t whatsmeow-api .
docker build -f deploy/Dockerfile -t whatsmeow-gateway .
docker build -f deploy/Dockerfile.web -t whatsmeow-frontend .
```

- `deploy/docker-compose.selfhost.yml` — one-box self-host install: one app container (API + gateway + frontend) + MySQL + Redis.
- `deploy/docker-compose.yml` — local full stack; only API HTTP/gRPC and frontend ports are published.
- `deploy/docker-compose.external.yml` — modular app containers over your own MySQL + Redis.
- `deploy/docker-compose.dev.yml` — infra only, for the host dev loop above.

The frontend runs anywhere it can reach MySQL and the API. The API is the public base URL.
Gateways run near WhatsApp with their `/data/keystore` volumes and a `GATEWAY_PUBLIC_URL` reachable by the
API. The full deployment guide is in `web/content/docs/dev/deploying.mdx`; the full env reference
lives in `deploy/.env.example` and `web/.env.example`.

## Repo layout

```
backend/       Go module (API, gateway, migrations, protobufs, generated code)
  cmd/api/     API entrypoint
  cmd/gateway/ gateway runtime entrypoint
  cmd/migrate/ WA schema migration command
  internal/    shared Go packages
  migrations/  embedded WA schema migrations
  proto/       public and private gRPC contracts
  gen/         generated Go protobuf bindings
web/           frontend: TanStack Start + better-auth + Drizzle; docs site
deploy/        Dockerfiles, Compose topologies, environment examples
docs/          shared OpenAPI contract and subsystem specifications
```

Root `make` targets coordinate both workspaces. Run Go commands with
`go -C backend …`, or from inside `backend/`. `make api`, `make dev`, and
`make migrate` keep the repository root as the runtime working directory so
existing `.env`, `deploy/.env`, and relative data paths retain their meaning.
Docker builds still use the repository root as their context.

## For contributors

Start with the design spec in [`masterplan-mvp.md`](./masterplan-mvp.md), then the per-subsystem
specs in [`docs/specs/`](./docs/specs/) — [`_V2-STATUS.md`](./docs/specs/_V2-STATUS.md) maps them
out. The API contract of record is [`docs/openapi.yaml`](./docs/openapi.yaml).

Keep both halves green:

```sh
go -C backend build ./... && go -C backend test ./...                    # gateway
cd web && pnpm build && pnpm typecheck && pnpm test # frontend
```

`scripts/smoke.sh` drives the trust seam end to end against a running stack — register a user, mint
a JWT, call the API with a Bearer token, create a session, fetch its pairing QR — and fails
loudly on any unexpected status. The pair → send → stream steps need a real phone and print as
manual instructions at the end.

```sh
BETTER_AUTH_URL=http://localhost:3000 API_URL=http://localhost:8090 scripts/smoke.sh
```

The v1 single-binary version (embedded auth, React SPA, MySQL keystore) is preserved at git tag
`mvp-v1`.
