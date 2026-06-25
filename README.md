# whatsmeow Gateway

A self-hostable, multi-tenant WhatsApp gateway built on
[whatsmeow](https://github.com/tulir/whatsmeow). A single Go binary serves both a
clean JSON REST API and an embedded React Router + shadcn dashboard. WAHA-class
capability, cleaner API, free — plus a first-class **Contacts** feature (who each
account has encountered, and where).

> **Legal / risk notice:** This uses an unofficial WhatsApp client. WhatsApp
> prohibits bots/unofficial clients; automated use may violate its Terms and get
> numbers **banned**. Built-in rate limiting and human-mimicry reduce but don't
> remove the risk. Use at your own risk.

## Overview

**Goals (v1)**

- Programmatic WhatsApp over whatsmeow for DMs + groups: text, replies, mentions,
  reactions, edit, delete (revoke), polls (+ vote events), location, contact cards.
- Multi-tenant: users self-register, attach their own number(s), consume events
  programmatically. User panel toggleable via ENV.
- Admin panel (super-admin) via Authula's Admin plugin; an ENV-provisioned admin
  WhatsApp number that doubles as a normal API-usable number.
- WhatsApp auth via QR or pairing code.
- Two delivery mechanisms: an HTTP chunked **NDJSON stream** (for consumers
  without a public URL) and **webhooks** (HMAC-signed, retried).
- REST send API + account-global **API keys** with permissions; sessions targeted
  by path.
- MySQL for messages + a rich **identity/contacts** model; Redis for queue, rate
  limits, stream fan-out, idempotency, cache.
- Read-only WhatsApp viewer; realtime-ish dashboard.
- Docker compose, two modes: DB-included (local) and external-DB (prod).

**Non-goals (v1):** media download/upload (inbound media = metadata only; media
sends return `501`); horizontal scaling; proxy-per-session; WhatsApp-as-login
(`amlogin`, plumbing only); Business labels.

The full design lives in [`masterplan-mvp.md`](./masterplan-mvp.md); per-subsystem
specs live in [`docs/specs/`](./docs/specs/); the API contract of record is
[`docs/openapi.yaml`](./docs/openapi.yaml).

## Build deviations from the masterplan

For local reliability the build deviates from the spec in two small ways:

1. **Module path** is `github.com/ramaadi/quick-whatsapp-gateway`.
2. **The pure-Go `modernc.org/sqlite` driver** is used for the SQLite keystore
   *fallback* (driver name `sqlite`), not the CGO `mattn/go-sqlite3`. Combined with
   Authula's library packages building cleanly without CGO, the whole project
   compiles with `CGO_ENABLED=0` — the Dockerfile needs no `gcc`/`musl-dev` and the
   runtime image is a fully static binary. The `.air.toml` build also uses
   `CGO_ENABLED=0`.

**Go toolchain:** the project targets `go 1.26.4` (matching the spec and required by
`github.com/Authula/authula`, note the capital `A`). A host Go ≥ 1.23 with
`GOTOOLCHAIN=auto` (the default) transparently fetches and uses 1.26.x; CI can pin
`GOTOOLCHAIN=local` once the host has 1.26. The Dockerfile builder is
`golang:1.26-alpine`.

**Authula reality vs. the masterplan:** Authula's published RBAC is a generic
roles+permissions model (no built-in `super_admin`/`user` constants — the gateway
seeds those roles itself), and its "Admin" plugin manages users/ban/impersonation
rather than "tenant" CRUD. The whatsmeow device keystore is implemented directly
against **MySQL** (whatsmeow's `dbutil` ships only SQLite/Postgres dialects, so the
MySQL backend hand-implements the `store.*` interfaces); SQLite via `sqlstore`
remains the `WHATSMEOW_STORE_DRIVER=sqlite` fallback. See `docs/specs/` for details.

## Quickstart (local development)

Infra runs in Docker; the app runs on the host for a fast edit loop.

```sh
cp deploy/.env.example .env     # then edit secrets (APP_ENCRYPTION_KEY etc.)
make infra-up                   # start MySQL + Redis (ports bound to localhost)
make dev                        # backend hot-reload via air (CGO_ENABLED=0)
make web                        # in a second terminal: frontend dev server (HMR)
```

Open the Vite dev URL; `/api`, `/auth`, and the NDJSON event stream proxy to the
Go server on `:8080`. `make infra-reset` wipes the dev database.

**Host prerequisites:** Go 1.23+, Node 22+ with pnpm (`corepack enable`),
[`air`](https://github.com/air-verse/air), `golangci-lint`, Docker. With the
pure-Go SQLite driver and the MySQL keystore, no C compiler is required.

## Production

```sh
make build                      # docker build -t whatsmeow-gateway -f deploy/Dockerfile .
```

- `deploy/docker-compose.yml` — local, all-in-one with MySQL + Redis included.
- `deploy/docker-compose.external.yml` — prod, bring-your-own MySQL + Redis.

## Repo layout

See masterplan §15. In short: `cmd/server` (entrypoint), `internal/*` (config,
http, auth, wa, store, webhooks, stream, queue), `migrations/` (golang-migrate),
`web/` (SPA), `deploy/` (Docker + compose), `docs/` (openapi + specs).
