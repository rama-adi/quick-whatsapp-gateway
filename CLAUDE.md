# CLAUDE.md

Repo guide for agents and contributors working on this v2 WhatsApp gateway. Read this before
making a change, so the code, the specs, the API contract, and the docs site stay in sync.

The system contains independently deployable API, gateway, web dashboard, and marketing/docs services:

- **API** (`backend/cmd/api/`) — caller authentication, org authorization, MySQL application data, Redis, public REST/gRPC, and private mTLS gateway coordination.
- **Gateway** (`backend/cmd/gateway/`) — live WhatsApp connections, SQLite keystore and journal, and private gRPC engine commands.
- **Web** (`web/`) — TanStack Start dashboard with better-auth identity and user-facing MDX docs.
- **Marketing** (`site-marketing/`) — TanStack Start landing page and Fumadocs MDX for hosters, platform admins, and developers.

Design rationale lives in [`site-marketing/content/docs/architecture/overview.mdx`](./site-marketing/content/docs/architecture/overview.mdx). This file is the bookkeeping
rulebook: where things live, what to update alongside a change, and the gates that must pass.

## Where things live

| Path | What it is |
|---|---|
| `site-marketing/content/docs/architecture/overview.mdx` | The v2 design spec — the overview every other doc drills into. |
| `site-marketing/content/docs/architecture/*.mdx` | One living spec per subsystem (detail). Start at `internal-docs/specification-status.md` (index of all specs + their state). |
| `docs/openapi.yaml` | The **public API contract of record, served by the router** at `/api/v1/openapi.yaml`. **GENERATED, not hand-written** (code-first via huma, D11): the Go input/output structs (`backend/internal/apitypes` + the per-resource `*_ops.go` registrars) are the source of truth; `make openapi` regenerates this file. Stays at repo root (shared system contract). |
| `internal-docs/mvp-progress.md` | Milestone tracker (R0–R6) and the log of locked decisions. |
| `web/content/docs/*` | User-facing Fumadocs guides and generated non-admin API reference. |
| `site-marketing/` | Standalone TanStack Start marketing site and Fumadocs MDX for operators, platform admins, and developers. |
| `web/` | Frontend — TanStack Start, better-auth, Drizzle, ported shadcn. |
| `backend/cmd/gateway/` | Gateway runtime entrypoint; never executes MySQL schema migrations. |
| `backend/cmd/api/` | API front door; applies WA schema migrations before serving. |
| `backend/cmd/migrate/` | Dedicated WA schema migration command (`up\|down`). |
| `backend/internal/` | Shared packages: `router/` (REST broker: authn, session→gateway resolve + org isolation, gRPC dispatch, placement), `authz/` (JWKS+JWT+api-key verify — **now consumed by the router**), `controlbus/` (`ctrl:*` subscriber — **now consumed by the router**), `dbconn/` (shared MySQL connection helper), `http/`, `wa/` (manager, session, SQLite store), `store/` (MySQL repos, org-keyed), `webhooks/`, `stream/`, `queue/`. |
| `backend/migrations/` | API-owned golang-migrate files for WA app-data tables. |
| `deploy/` | Gateway, API, frontend, dev, and self-host Dockerfiles; compose topologies; `.env.example`. |

### The subsystem specs (`site-marketing/content/docs/architecture/`)

| Spec | Covers |
|---|---|
| `router.mdx` | The central router: front door + single trust boundary, REST broker (placement / session-owner routing / `503 gateway_unavailable`), Private gRPC dispatch, registry lifecycle. |
| `trust-model.mdx` | The two caller identities, org ownership, control bus + cache + revocation, boot orphan-guard. (Authn + control-bus now run on the router; the gateway authenticates the API over mTLS.) |
| `api-keys.mdx` | API verifying better-auth api-keys against the shared `apikey` table. |
| `whatsmeow-store.mdx` | The whatsmeow keystore on gateway-local SQLite (`modernc.org/sqlite`, CGO=0). |
| `session-manager.mdx` | Session lifecycle, `gateway_id` pinning, boot orphan-guard. |
| `store.mdx` | MySQL WA-data schema + repos, org-keyed ownership. |
| `http-foundation.mdx` | The HTTP layer: two-acceptor authz middleware, CORS, route map. |
| `stream.mdx` | The realtime WebSocket event stream. |
| `webhooks.mdx` | Outbound webhook config, HMAC, retries. |
| `eventing.mdx` | The event envelope + catalog. |
| `queue.mdx` | Redis work queue vs control bus, key/channel prefixes. |
| `inbound-pipeline.mdx` | Inbound message handling. |
| `outbound-pipeline.mdx` | Outbound send pipeline + idempotency. |
| `resources.mdx` | Resource model + session API responses. |
| `contacts.mdx` | The contacts feature. |
| `frontend.mdx` | The TanStack Start + better-auth frontend. |

## Bookkeeping rules

The specs and the OpenAPI file are part of the code, not an afterthought. The masterplan makes this
a hard convention (§20, "Documentation" and "Commits" bullets):

> Change a subsystem's behavior, update its `site-marketing/content/docs/architecture/*.mdx` in the **same change**. The
> masterplan is the overview, the specs are the detail, `openapi.yaml` is the API contract of
> record.

Follow-on steps depend on what you touched. Run them in the same change as the behavior:

| You changed… | Then also run / write |
|---|---|
| The public REST API (paths, request/response shapes) | Edit the **Go types**, not the yaml: the per-resource huma ops in `backend/internal/http/handlers/*_ops.go` (operations + request/response structs with `doc:`/`enum:`/`example:` tags) and shared DTOs/events in `backend/internal/apitypes`. Then `make openapi` (regenerates `docs/openapi.yaml` from the Go types — the contract of record the router serves), then `cd web && pnpm gen:api` (regen typed client `app/lib/api/schema.d.ts`) **and** `pnpm docs:openapi`; also run `cd site-marketing && pnpm docs:openapi` (regen the fumadocs API reference pages). `make gen` regenerates the contract, client types, and both API documentation trees. CI guards drift with `make openapi-check`. Webhook/realtime **event** shapes live in `backend/internal/apitypes/events.go` (the generated OpenAPI `webhooks` section). |
| better-auth config (`web/app/lib/auth/server.ts`) | `cd web && pnpm auth:generate` (regen `app/lib/db/auth-schema.ts`), then `pnpm db:migrate` (drizzle-kit) to apply the auth tables. |
| The WA app-data MySQL schema | Author a new `backend/migrations/NNNN_*.{up,down}.sql` (golang-migrate), then `cd web && pnpm db:introspect`. |

### Two migration toolchains — don't cross them

The shared MySQL has two writers, each with its own tool. Run the right one for the table you are
changing:

| Tables | Owner | Tool | Command |
|---|---|---|---|
| WA app-data (gateways, sessions, contacts, …) | API/control plane | golang-migrate | API startup `up`; `make migrate` builds `backend/cmd/migrate` and runs it from repo root; `.dev/migrate down` rolls back one |
| Auth (better-auth: user, session, apikey, organization, …) | Frontend | drizzle-kit | `cd web && pnpm db:migrate` |

The API/control plane's golang-migrate is the **sole schema writer** of WA tables. The frontend only ever
*introspects* them into read-only Drizzle models (`pnpm db:introspect`) — it never migrates them.

### Pre-release: reshape the schema freely

The software has **not shipped** — there is no production data and no backward-compat burden.
**A schema change that makes the design cleaner is encouraged, not avoided.** If a table's shape is
awkward, fix the shape: rewrite the migration, add columns, drop/rename, or **drop a table and
rebuild it** when a cleaner model exists. Prefer a correct, normalized design now over a workaround
that we carry forever.

Guidance, not friction:

- Don't accumulate compatibility shims or "v2.5" half-migrations for a DB nobody depends on yet —
  collapse them into the cleanest end state.
- A wholesale reshape can still be a single fresh migration (we already did this: `0001_init`
  replaced the v1 migrations against an empty DB). Truncating/rebuilding dev tables is fine.
- Still obey the **bookkeeping rules above**: a schema change updates `site-marketing/content/docs/architecture/*` (esp.
  `store.mdx`), runs `pnpm db:introspect` to refresh the read-only WA Drizzle models, and updates
  `docs/openapi.yaml` + regenerates if it changes a REST response shape.
- The line that does **not** move: WA tables are migrated only by API-owned golang-migrate
  (the frontend introspects, never migrates), and auth tables only by drizzle-kit. Reshape freely
  **within** the right toolchain — don't cross them.
- Caveat: when in doubt whether a denormalization actually helps, prefer **read-time resolution
  from a single source of truth** over copying derived data onto rows (e.g. message sender/mention
  names are resolved from `whatsapp_identities` on read, not stored on `messages`, so a rename is
  reflected without rewrites). "Cleaner" means more normalized and correct, not more copies.

### v1 is archived

The v1 single-binary build (Authula auth, embedded React Router SPA, MySQL keystore) is preserved
at git tag `mvp-v1`. Anything v1-shaped still in the working tree is a removable duplicate. Don't
resurrect v1 code — check out the tag if you need to read it.

## Green gates before commit

The backend and web app must build and pass tests at every committed step. Changes to site-marketing must also pass its build and typecheck.

**Backend** (from repo root; module lives in `backend/`):

```sh
go -C backend build ./...
go -C backend vet ./...
go -C backend test ./...
```

**Frontend** (from `web/`):

```sh
pnpm build
pnpm typecheck
pnpm test
```

**Marketing docs**: `pnpm --dir site-marketing build` and `pnpm --dir site-marketing typecheck`.

`golangci-lint run` (or `make lint`) is the gateway linter. The trust seam — better-auth's api-key
hash and the EdDSA JWT shape — is pinned by contract tests in `backend/internal/authz/`
(`contract_test.go`, `jwt_test.go`); regenerate their fixtures if the pinned better-auth version
changes.

## Commits

- Conventional-Commits prefixes (`feat:`, `fix:`, `docs:`, `chore:`, …).
- Small, green increments — both halves pass the gates above before you commit.
- Commit from the repo root with `git add -A`; the tree should contain only that change's intended
  edits, including the spec/OpenAPI/doc updates the change required.

## Where notes and decisions go

- A **design decision** (an alternative weighed, a tradeoff locked) → the relevant subsystem spec
  in `site-marketing/content/docs/architecture/`, or `site-marketing/content/docs/architecture/overview.mdx` if it spans the whole system.
- A **milestone status change or a session-level locked decision** → `internal-docs/mvp-progress.md` (it has
  a "Key v2 decisions" section and an "Open risks / follow-ups" section).
- **User-facing how-to** → MDX under `web/content/docs/guides/`.
- **Hoster, platform-admin, or developer how-to** → MDX under `site-marketing/content/docs/operators/`.
- Keep all documentation in these two Fumadocs trees; do not create standalone Markdown docs. Repository instruction files are the exception.

Keep each in **one place and current** — update the living doc in place rather than appending a new
note that the reader has to reconcile against the old one.

Internal project plans, progress records, and historical handoffs live in `internal-docs/*.md` and remain outside the published documentation sites.
