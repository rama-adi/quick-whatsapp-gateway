# MSW — the kernel

## program — complete

```
contract ← the requested outcome + the smallest criteria that prove it

while ∃ claim c : deleting c leaves contract unmet ∨ unproven
      do c ; prove c

halt ; report
```

## definitions — no behavior lives here, only meaning

**contract** — the requested outcome and the smallest set of acceptance criteria that would prove it, stated before any work. The sole source of necessity; a ceiling as much as a floor. If the request is ambiguous: attended → ask; unattended → bind the smallest reading consistent with stated intent and record the assumption.

**claim** — anything petitioning to become work: a plan step, a change, a test, a reviewer's P1, a discovered edge case, your own instinct that one more pass would help. Everything enters as this type. Nothing enters as a verdict.

**deleting c leaves contract unmet ∨ unproven** — the only test. A claim passes solely by breaking the contract — reproducibly, within the task's actual inputs and environment. Severity is derived from the contract, never inherited from whoever raised the claim. *Useful*, *thorough*, and *possible* are not aliases for *necessary*. A claim that fails receives one line in the report — never a fix, an investigation, or a deferred follow-up.

**do ; prove** — the smallest reliable act that closes the gap, and evidence sized to the claim it settles. An unproven act keeps its claim alive; a proven one closes it — and re-proving a closed claim is itself an inadmissible claim.

**halt** — the fixed point: contract proven, no remaining claim passes. Not reviewer silence; not exhausted imagination. Halting before the fixed point and looping past it are the same bug, mirrored.

**report** — the outcome against the contract; the proof; rejected claims worth the user's attention, one line each. Nothing else.

## fuses — outside the program, for when its evaluator fails

```
rounds = 3            → halt anyway ; report open items, do not chase them
claim born in round n+1, visible in round n   → rejected
```

## No unauthoritative limits

Never invent a limit. A cap, threshold, quota, budget, timeout, retry or round count, file or line count, acceptance-criterion count, agent count, or similar constraint is admissible only when its exact value is:

- explicitly required by the requester;
- imposed by an applicable technical or platform contract;
- defined by authoritative project policy; or
- derived from measured evidence necessary to meet or prove the task contract.

State the authority or derivation whenever proposing or applying a limit. If no authority exists, omit the limit and use the MSW necessity test. Metrics may be reported as evidence, but they must not become gates, defaults, targets, or recommendations through agent intuition. Examples and representative proportions never become defaults. If a necessary limit is an unresolved owner choice, ask; do not manufacture a value.

---

# Repo guide

This is a v2 WhatsApp gateway: two independently deployable services in one repo.

- **Gateway** (Go, `cmd/` + `internal/` + `migrations/`) — the whatsmeow engine. Verifies
  caller identity minted by the frontend (better-auth JWTs via JWKS, better-auth api-keys),
  owns WA-domain MySQL tables, keeps the whatsmeow keystore in gateway-local SQLite.
- **Frontend** (`web/`) — a TanStack Start app with better-auth for identity. Serverless-hostable;
  the browser talks to the gateway directly.

Design rationale lives in [`masterplan-mvp.md`](./masterplan-mvp.md). This file is the bookkeeping
rulebook: where things live, what to update alongside a change, and the gates that must pass.
These rules are the project policy the MSW contract binds to: an edit the rules require in the
same change (spec, generated file, migration counterpart) is part of the contract, not optional
extra work.

## Where things live

| Path | What it is |
|---|---|
| `masterplan-mvp.md` | The v2 design spec — the overview every other doc drills into. |
| `docs/specs/*.md` | One living spec per subsystem (detail). Start at `_V2-STATUS.md` (index of all specs + their state). |
| `docs/openapi.yaml` | The public API contract of record, served by the router at `/api/v1/openapi.yaml`. **Generated** — see the bookkeeping table below for how. Stays at repo root (shared system contract). |
| `docs/mvp-progress.md` | Milestone tracker (R0–R6) and the log of locked decisions. |
| `web/content/docs/*` | The fumadocs site: hand-written user/dev guides (`guides/`) + generated API reference (`api/`). |
| `web/` | Frontend — TanStack Start, better-auth, Drizzle, ported shadcn. |
| `cmd/server/` | Gateway entrypoint; also `server migrate up\|down`. |
| `cmd/router/` | Router entrypoint — the front door + single trust boundary in front of the gateways. |
| `internal/` | Shared packages: `router/` (REST broker: authn, session→gateway resolve + org isolation, reverse proxy, placement), `assertion/` (router→gateway request-bound Ed25519 internal assertion: minter/verifier/nonce-cache), `authz/` (JWKS+JWT+api-key verify — **now consumed by the router**), `controlbus/` (`ctrl:*` subscriber — **now consumed by the router**), `dbconn/` (shared MySQL connection helper), `http/`, `wa/` (manager, session, SQLite store), `store/` (MySQL repos, org-keyed), `webhooks/`, `stream/`, `queue/`. |
| `migrations/` | golang-migrate files for WA app-data tables (gateway-written MySQL). |
| `deploy/` | Two Dockerfiles, compose files, `.env.example`. |

### The subsystem specs (`docs/specs/`)

| Spec | Covers |
|---|---|
| `router.md` | The central router: front door + single trust boundary, REST broker (placement / session-owner routing / `503 gateway_unavailable`), Ed25519 internal assertion, registry lifecycle. |
| `trust-model.md` | The two caller identities, org ownership, control bus + cache + revocation, boot orphan-guard. (Authn + control-bus now run on the router; the gateway trusts the router's assertion.) |
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

The specs and the OpenAPI file are part of the code: change a subsystem's behavior, update its
`docs/specs/*.md` in the **same change**. The masterplan is the overview, the specs are the
detail, `openapi.yaml` is the API contract of record.

Follow-on steps depend on what you touched. Run them in the same change as the behavior:

| You changed… | Then also run / write |
|---|---|
| The public REST API (paths, request/response shapes) | Edit the **Go types**, not the yaml: the per-resource huma ops in `internal/http/handlers/*_ops.go` (operations + request/response structs with `doc:`/`enum:`/`example:` tags) and shared DTOs/events in `internal/apitypes`. Then `make openapi` (regenerates `docs/openapi.yaml` from the Go types), then `cd web && pnpm gen:api` (regen typed client `app/lib/api/schema.d.ts`) **and** `pnpm docs:openapi` (regen the fumadocs API reference pages). `make gen` runs all three. CI guards drift with `make openapi-check`. Webhook/realtime **event** shapes live in `internal/apitypes/events.go` (the generated OpenAPI `webhooks` section). |
| better-auth config (`web/app/lib/auth/server.ts`) | `cd web && pnpm auth:generate` (regen `app/lib/db/auth-schema.ts`), then `pnpm db:migrate` (drizzle-kit) to apply the auth tables. |
| The gateway MySQL schema | Author a new `migrations/NNNN_*.{up,down}.sql` (golang-migrate), then `cd web && pnpm db:introspect` to refresh the read-only WA Drizzle models (`app/lib/db/wa.ts`). Update `docs/specs/store.md`; if a REST response shape changed, the REST-API row above also applies. |

### Two migration toolchains — don't cross them

The shared MySQL has two writers, each with its own tool. Run the right one for the table you are
changing:

| Tables | Owner | Tool | Command |
|---|---|---|---|
| WA app-data (gateways, sessions, contacts, …) | Gateway | golang-migrate (embedded in the binary) | `make migrate` → `go run ./cmd/server migrate up` (`down` rolls back one) |
| Auth (better-auth: user, session, apikey, organization, …) | Frontend | drizzle-kit | `cd web && pnpm db:migrate` |

The gateway's golang-migrate is the **sole writer** of WA tables; the frontend only ever
introspects them into read-only Drizzle models (`pnpm db:introspect`). Reshape freely **within**
the right toolchain — never across.

### Existing deployment and clean target architecture

The owner runs one deployment on Zeabur. Keep the clean target architecture;
do not add legacy runtime paths or compatibility schema migrations solely for
that installation. Handle its existing data with a separately verified one-time
operator upgrade. Preserve its gateway IDs, paired devices, and SQLite keystore.

Before pushing a runtime cutover to main, verify Zeabur's deployment trigger,
backups, configuration, and enrollment readiness. A green build alone does not
prove the running deployment is ready to switch.

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
- Small, green increments.
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