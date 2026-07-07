# Sign in with WhatsApp (OAuth 2.1 / OIDC provider) — progress tracker

Design of record: [`docs/specs/oauth.md`](./docs/specs/oauth.md). This file tracks the
implementation milestones (design §11). Each increment lands green (gateway
`go build/vet/test`; web `pnpm build/typecheck/test`) and is committed on its own.

Owners: **B** = gpt-5.5/Codex (backend rigor) · **F** = opus-4.8 (frontend/copy/docs) ·
orchestration + reviews = Fable.

| # | Increment | Owner | Status | Notes |
|---|---|---|---|---|
| 0 | Design synthesis + spec (`docs/specs/oauth.md`) + this tracker | Fable | ✅ done | Opus + gpt-5.5 parallel designs, arbitrated (spec §12). |
| 1 | Migration `0007_oidc_provider` + `internal/store/oauth.go` repos + `pnpm db:introspect` | B | ✅ done | Four tables, plain-SQL repos, store tests. `pnpm db:introspect` attempted; skipped because local MySQL was unreachable (`EPERM 127.0.0.1:3306`). |
| 2 | Signing keys + `oidp.Signer` + `rotate-key` subcommand + discovery + JWKS | B | ✅ done | Dedicated EdDSA keyset, AES-GCM at rest, one JWKS across replicas; focused signer/router tests pass. |
| 3 | Management CRUD (huma ops) + org isolation + secret hashing + `make gen` | B | ✅ done | Router-local `/api/v1/oauth-apps*`; secret shown once. `pnpm docs:openapi` blocked by local Node lacking `--experimental-strip-types`. |
| 4 | Dashboard OAuth-apps UI | F | ⬜ pending | List / editor (consent-card preview, login-command field) / secret-once modal / grants tab / integration-guide tab. |
| 5 | `/oauth/authorize` + pending model + NDJSON wait stream + cancel + consent page | B (endpoints) + F (page) | ✅ done | Consent page merged (22 tests) + router endpoints (`internal/oidp/provider.go`, `pending.go`): authorize validation matrix, two-code mint, NDJSON stream matching the pinned §4.2 contract frame-for-frame, cancel, mint rate limit. Live end-to-end visual pass deferred to M6/M7 integration. |
| 6 | Inbound `LoginInterceptor` + Redis Lua claim + publish + bot reactions + STOP | B | ⬜ pending | Per-app `login_command`; unconditional interception; `-race` tests on claim. |
| 7 | Finalize + `/oauth/token` (PKCE, refresh rotation + reuse-kill) + userinfo + revoke | B | ⬜ pending | Verified end-to-end with an off-the-shelf OIDC client. |
| 8 | Grants dashboard + revocation cascades + `ctrl:oidp.*` propagation | F (UI) + B (cascades) | ⬜ pending | |
| 9 | Hardening (rate limits, stream caps, phishing copy) + `guides/sign-in-with-whatsapp.md` + spec finalization | B + F | ⬜ pending | Final adversarial review of spec §7 before ship. |

## Log

- **2026-07-08** — Design locked and committed (`docs/specs/oauth.md`), tracker created.
  Key arbitrations (spec §12): Redis Lua claim over reverse assertion; auth code minted at
  browser-driven finalize; pairwise subs; dedicated EdDSA signing keys; per-app `login_command`
  with unconditional command-namespace interception.
- **2026-07-08** — Milestone 1 landed: added `0007_oidc_provider` with the four spec tables,
  plain-SQL OAuth repos under `internal/store/oauth.go`, Store wiring, and repo tests. Drizzle
  introspection was attempted but skipped because local MySQL was not reachable from this sandbox
  (`connect EPERM 127.0.0.1:3306`).
- **2026-07-08** — Milestone 2 landed in the working tree: added `internal/oidp.Signer`,
  AES-GCM encrypted private JWK storage, EdDSA JWT signing, JWKS publication, router-local OIDC
  discovery endpoints, `cmd/router oidp rotate-key`, and env docs. Focused tests verify signed
  JWTs against the served JWKS and the rotation state machine's one-active-key invariant.
- **2026-07-08** — Milestone 3 landed in the working tree: added router-local, org-scoped
  OAuth app management CRUD under `/api/v1/oauth-apps*`, including redirect URI and
  `login_command` validation, session ownership checks, one-time confidential client secrets
  hashed with `OAUTH_CLIENT_SECRET_PEPPER`, enable/disable, secret rotation, grant listing, and
  grant/token revocation cascades. `make openapi` and `pnpm gen:api` completed; `pnpm
  docs:openapi` was skipped because this sandbox's Node rejected `--experimental-strip-types`.
- **2026-07-08** — Milestones 1–3 committed on `mvp/oauth2-server` (work moved off `main`);
  `pnpm docs:openapi` re-run successfully by the orchestrator; full gateway + web gates green.
- **2026-07-08** — Milestone 5-F merged: public consent/waiting page (fragment code, NDJSON
  stream driver with reconnect, `wa.me` deep link + QR via `uqr`, countdown, cancel/terminal
  states, §7.3 warning copy). Wire contract the page assumes is now pinned in spec §4.2.
  Known gap: `.claude/launch.json` preview resolves against the main checkout, so worktree
  branches can't be preview-verified; consent card verified via unit tests + route smoke only —
  full visual pass due in milestone 5-B integration.
- **2026-07-08** — Milestone 5-B landed: `/oauth/authorize` (full validation matrix, two-code
  mint with pattern rejection + per-session mint rate limit), Redis pending store + reverse
  index + pub/sub helpers, `GET /oauth/wait/{code}/stream` (NDJSON, snapshot frame asserted
  key-for-key against the consent page's `protocol.ts`), `/cancel`, config for `WEB_LOGIN_URL`
  + OIDC TTLs. Lua claim primitive scaffolded with the M6 signature (wrong-attempt accounting
  deliberately deferred to M6).
