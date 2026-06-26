# CLAUDE.md

Repo guide for agents and contributors working on this v2 WhatsApp gateway. Read this before
making a change, so the code, the specs, the API contract, and the docs site stay in sync.

The system is two independently deployable services in one repo:

- **Gateway** (Go, `cmd/` + `internal/` + `migrations/`) — the whatsmeow engine. Verifies
  caller identity minted by the frontend (better-auth JWTs via JWKS, better-auth api-keys),
  owns WA-domain MySQL tables, keeps the whatsmeow keystore in gateway-local SQLite.
- **Frontend** (`web/`) — a TanStack Start app with better-auth for identity. Serverless-hostable;
  the browser talks to the gateway directly.

Design rationale lives in [`masterplan-mvp.md`](./masterplan-mvp.md). This file is the bookkeeping
rulebook: where things live, what to update alongside a change, and the gates that must pass.

## Where things live

| Path | What it is |
|---|---|
| `masterplan-mvp.md` | The v2 design spec — the overview every other doc drills into. |
| `docs/specs/*.md` | One living spec per subsystem (detail). Start at `_V2-STATUS.md` (index of all specs + their state). |
| `docs/openapi.yaml` | The API contract of record for the gateway REST API. |
| `docs/mvp-progress.md` | Milestone tracker (R0–R6) and the log of locked decisions. |
| `web/content/docs/*` | The fumadocs site: hand-written user/dev guides (`guides/`) + generated API reference (`api/`). |
| `web/` | Frontend — TanStack Start, better-auth, Drizzle, ported shadcn. |
| `cmd/server/` | Gateway entrypoint; also `server migrate up\|down`. |
| `internal/` | Gateway packages: `authz/` (JWKS+JWT+api-key verify), `controlbus/`, `http/`, `wa/` (manager, session, SQLite store), `store/` (MySQL repos, org-keyed), `webhooks/`, `stream/`, `queue/`. |
| `migrations/` | golang-migrate files for WA app-data tables (gateway-written MySQL). |
| `deploy/` | Two Dockerfiles, compose files, `.env.example`. |

### The subsystem specs (`docs/specs/`)

| Spec | Covers |
|---|---|
| `trust-model.md` | The two caller identities, org ownership, control bus + cache + revocation, boot orphan-guard. |
| `api-keys.md` | Gateway verifying better-auth api-keys against the shared `apikey` table. |
| `whatsmeow-store.md` | The whatsmeow keystore on gateway-local SQLite (`modernc.org/sqlite`, CGO=0). |
| `session-manager.md` | Session lifecycle, `gateway_id` pinning, boot orphan-guard. |
| `store.md` | MySQL WA-data schema + repos, org-keyed ownership. |
| `http-foundation.md` | The HTTP layer: two-acceptor authz middleware, CORS, route map. |
| `stream.md` | The NDJSON event stream. |
| `webhooks.md` | Outbound webhook config, HMAC, retries. |
| `eventing.md` | The event envelope + catalog. |
| `queue.md` | Redis work queue vs control bus, key/channel prefixes. |
| `inbound-pipeline.md` | Inbound message handling. |
| `outbound-pipeline.md` | Outbound send pipeline + idempotency. |
| `resources.md` | Resource model + session API responses. |
| `contacts.md` | The contacts feature. |
| `frontend.md` | The TanStack Start + better-auth frontend. |

## Bookkeeping rules

The specs and the OpenAPI file are part of the code, not an afterthought. The masterplan makes this
a hard convention (§20, "Documentation" and "Commits" bullets):

> Change a subsystem's behavior, update its `docs/specs/*.md` in the **same change**. The
> masterplan is the overview, the specs are the detail, `openapi.yaml` is the API contract of
> record.

Follow-on steps depend on what you touched. Run them in the same change as the behavior:

| You changed… | Then also run / write |
|---|---|
| The gateway REST API (paths, request/response shapes) | Edit `docs/openapi.yaml`, then `cd web && pnpm gen:api` (regen typed client `app/lib/api/schema.d.ts`) **and** `pnpm docs:openapi` (regen the fumadocs API reference pages under `content/docs/api/`). |
| better-auth config (`web/app/lib/auth/server.ts`) | `cd web && pnpm auth:generate` (regen `app/lib/db/auth-schema.ts`), then `pnpm db:migrate` (drizzle-kit) to apply the auth tables. |
| The gateway MySQL schema | Author a new `migrations/NNNN_*.{up,down}.sql` (golang-migrate), then `cd web && pnpm db:introspect` to refresh the read-only WA Drizzle models (`app/lib/db/wa.ts`). |

### Two migration toolchains — don't cross them

The shared MySQL has two writers, each with its own tool. Run the right one for the table you are
changing:

| Tables | Owner | Tool | Command |
|---|---|---|---|
| WA app-data (gateways, sessions, contacts, …) | Gateway | golang-migrate (embedded in the binary) | `make migrate` → `go run ./cmd/server migrate up` (`down` rolls back one) |
| Auth (better-auth: user, session, apikey, organization, …) | Frontend | drizzle-kit | `cd web && pnpm db:migrate` |

The gateway's golang-migrate is the **sole writer** of WA tables. The frontend only ever
*introspects* them into read-only Drizzle models (`pnpm db:introspect`) — it never migrates them.

### v1 is archived

The v1 single-binary build (Authula auth, embedded React Router SPA, MySQL keystore) is preserved
at git tag `mvp-v1`. Anything v1-shaped still in the working tree is a removable duplicate. Don't
resurrect v1 code — check out the tag if you need to read it.

## Green gates before commit

Both halves must build and pass tests at every committed step.

**Gateway** (from repo root):

```sh
go build ./...
go vet ./...
go test ./...
```

**Frontend** (from `web/`):

```sh
pnpm build
pnpm typecheck
pnpm test
```

`golangci-lint run` (or `make lint`) is the gateway linter. The trust seam — better-auth's api-key
hash and the EdDSA JWT shape — is pinned by contract tests in `internal/authz/`
(`contract_test.go`, `jwt_test.go`); regenerate their fixtures if the pinned better-auth version
changes.

## Commits

- Conventional-Commits prefixes (`feat:`, `fix:`, `docs:`, `chore:`, …).
- Small, green increments — both halves pass the gates above before you commit.
- Commit from the repo root with `git add -A`; the tree should contain only that change's intended
  edits, including the spec/OpenAPI/doc updates the change required.

## Where notes and decisions go

- A **design decision** (an alternative weighed, a tradeoff locked) → the relevant subsystem spec
  in `docs/specs/`, or `masterplan-mvp.md` if it spans the whole system.
- A **milestone status change or a session-level locked decision** → `docs/mvp-progress.md` (it has
  a "Key v2 decisions" section and an "Open risks / follow-ups" section).
- **User- or developer-facing how-to** → a fumadocs page under `web/content/docs/`.

Keep each in **one place and current** — update the living doc in place rather than appending a new
note that the reader has to reconcile against the old one.
