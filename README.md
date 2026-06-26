# whatsmeow Gateway (v2)

A self-hostable, multi-tenant WhatsApp gateway built on
[whatsmeow](https://github.com/tulir/whatsmeow), split into **two independently deployable
services**:

- **Gateway** (Go) — the WhatsApp engine. Holds the live whatsmeow sessions, exposes a clean JSON
  REST API + an NDJSON event stream + webhooks. **No human login**: it verifies caller identity
  minted by the frontend (better-auth JWTs via JWKS, and better-auth api-keys). Pure-Go,
  `CGO_ENABLED=0`, static image.
- **Frontend** (`web/`) — a fullstack **TanStack Start** dashboard with **better-auth** for
  identity (email/password, 2FA, admin, api-keys, JWT, organizations). Serverless-hostable; the
  browser talks to the gateway directly.

WAHA-class capability, cleaner API, free — plus a first-class **Contacts** feature (who each
account has encountered, and where).

> **Legal / risk notice:** This uses an unofficial WhatsApp client. WhatsApp prohibits
> bots/unofficial clients; automated use may violate its Terms and get numbers **banned**.
> Built-in rate limiting and human-mimicry reduce but don't remove the risk. **Use at your own
> risk.**

## Why the split

v1 was a single Go binary with embedded auth (Authula), an embedded React SPA, and the whatsmeow
keystore in MySQL. v2 separates concerns so the frontend can run on serverless and the gateway can
run near WhatsApp:

- **Identity is the frontend's job.** better-auth mints short-lived (~5 min) EdDSA JWTs and issues
  org-scoped api-keys. The gateway **verifies** them locally — JWTs against the cached JWKS,
  api-keys against the shared `apikey` table — with **no per-request callback**. See
  [`docs/specs/trust-model.md`](docs/specs/trust-model.md).
- **Ownership is by organization.** Resources are owned by a better-auth `organization_id`; every
  user gets a personal org on signup, so "sharing a connection" = inviting someone into the org.
- **Three storage planes.** Auth tables (frontend, Drizzle) + WA app-data (gateway-written, MySQL)
  share one MySQL; the whatsmeow keystore is **gateway-local SQLite** on a persistent volume
  ([`docs/specs/whatsmeow-store.md`](docs/specs/whatsmeow-store.md)).
- **Hybrid reads.** The frontend reads WA tables directly (Drizzle) for fast dashboards, **acts**
  via the gateway REST API, and gets **realtime** from the gateway NDJSON stream.
- **Instant revocation** rides a cross-service Redis **control bus** (`ctrl:*`): revoke a key /
  ban a user in the dashboard and every gateway drops it within ~60 s (cache TTL backstop).

## Architecture

```
            better-auth JWT / api-key (Bearer)
  Browser ───────────────────────────────────────────►  Gateway (Go, whatsmeow)
     │  ▲                                                   │   ├─ JSON REST API  /api/v1
     │  │ session cookie + mint JWT (/api/auth/token)       │   ├─ NDJSON stream  /events
     ▼  │                                                   │   └─ webhooks (out)
  Frontend (TanStack Start + better-auth)                   │
     ├─ /api/auth/* (JWKS, token, admin, api-keys, orgs)    │  keystore → SQLite (volume)
     └─ direct read-only Drizzle reads ──┐                  │
                                         ▼                  ▼
                              MySQL  (auth tables ⇄ WA app-data tables)
                                         ▲                  │
   control bus: frontend PUBLISH ctrl:*  └── Redis ─────────┘  work: queue/rate-limit/stream
```

Full design: [`masterplan-mvp.md`](./masterplan-mvp.md). Per-subsystem specs:
[`docs/specs/`](./docs/specs/) (start with [`_V2-STATUS.md`](./docs/specs/_V2-STATUS.md)). API
contract of record: [`docs/openapi.yaml`](./docs/openapi.yaml).

## Quick start (local development)

Infra in Docker; both apps on the host for a fast edit loop.

```sh
cp deploy/.env.example .env     # then edit secrets (APP_ENCRYPTION_KEY, BETTER_AUTH_SECRET, …)
make infra-up                   # start MySQL + Redis (ports bound to localhost)
make migrate                    # apply WA-data migrations (the gateway binary: server migrate up)

make dev                        # terminal 1: gateway hot-reload via air (CGO_ENABLED=0)
make web                        # terminal 2: frontend dev server (pnpm dev, HMR)
# terminal 3 as needed: cd web && pnpm drizzle-kit migrate  (better-auth tables)
```

Open the frontend dev URL (`http://localhost:3000`). The browser calls the gateway directly on
`:8080` (CORS allows `http://localhost:3000`). `make infra-reset` wipes the dev database.

**Host prerequisites:** Go 1.26+ (`GOTOOLCHAIN=auto` fetches it), Node 22+ with pnpm
(`corepack enable`), [`air`](https://github.com/air-verse/air), `golangci-lint`, Docker. **No C
compiler** — the SQLite keystore uses the pure-Go `modernc.org/sqlite` driver, so the whole
project builds with `CGO_ENABLED=0`.

## Production

Two images, deployed independently:

```sh
make build                                   # gateway image (deploy/Dockerfile)
docker build -f deploy/Dockerfile.web -t whatsmeow-frontend .   # frontend image (.output)
```

- `deploy/docker-compose.yml` — local all-in-one (gateway + frontend + MySQL + Redis).
- `deploy/docker-compose.external.yml` — bring-your-own MySQL + Redis.
- `deploy/docker-compose.dev.yml` — infra only (MySQL + Redis) for the host dev loop.

The frontend can run anywhere (Vercel/Node/container) as long as it reaches MySQL and the gateway;
the gateway runs near WhatsApp with its `/data/keystore` volume. They need not be co-located —
that's the point of v2.

## Configuration

Gateway env (selected): `HTTP_ADDR`, `GATEWAY_ID`, `PUBLIC_URL`, `BETTER_AUTH_URL`,
`BETTER_AUTH_JWKS_URL`, `FRONTEND_ORIGINS`, `APP_ENCRYPTION_KEY`, `MYSQL_DSN`,
`WHATSMEOW_STORE_DSN`, `REDIS_URL`, `PUBSUB_REDIS_URL`, `WHATSAPP_ADMIN_NUMBER`. Frontend env:
`BETTER_AUTH_SECRET`, `BETTER_AUTH_URL`, `DATABASE_URL`, `GATEWAY_URL` (+ `VITE_GATEWAY_URL`),
`PUBSUB_REDIS_URL`, `USER_REGISTRATION_ENABLED`. Full table + defaults: masterplan §14;
`deploy/.env.example`.

## Smoke test

`scripts/smoke.sh` drives the automatable slice of the trust seam against a running stack
(register a user → mint a JWT from better-auth → call the gateway with `Bearer` → create a session
→ fetch its pairing QR), failing loudly on any unexpected HTTP status. The WhatsApp pair → send →
stream steps need a real phone and are printed as manual instructions at the end.

```sh
BETTER_AUTH_URL=http://localhost:3000 GATEWAY_URL=http://localhost:8080 \
  scripts/smoke.sh
```

The byte-level halves of that seam — better-auth's api-key hash + permissions JSON, and the EdDSA
JWT shape — are pinned as Go contract tests in `internal/authz/` (`contract_test.go`,
`jwt_test.go`); regenerate the fixtures if the pinned better-auth version changes.

## Repo layout

```
cmd/server/        gateway entrypoint (also: server migrate up|down)
internal/          gateway: authz/ (JWKS+JWT+api-key verify) · controlbus/ · http/ · wa/
                   (manager, session, store/sqlite) · store/ (MySQL repos, org-keyed) ·
                   webhooks/ · stream/ · queue/
migrations/        golang-migrate, WA app-data only (no wmstore_* in MySQL)
web/               frontend: TanStack Start + better-auth + Drizzle + ported shadcn
deploy/            Dockerfile · Dockerfile.web · compose files · .env.example
docs/              openapi.yaml · specs/*.md · mvp-progress.md · archive/ (v1 snapshot)
```

The v1 single-binary MVP is archived at git tag `mvp-v1` and under `docs/archive/`. Full layout:
masterplan §16.
