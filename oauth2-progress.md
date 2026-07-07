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
| 4 | Dashboard OAuth-apps UI | F | ✅ done | `/user/oauth-apps` list + detail (settings/grants/integration tabs), shared form with live `ConsentCard` preview, secret-once modal, group picker with JID fallback, Node quickstart. API gaps handed to M7/M8: grant display fields (`displayName`, `phoneMasked`, `refreshFamilyCount` on `OAuthGrant`), a bulk `revokeAllGrants` op, and surfacing `OIDC_ISSUER` to the client when it differs from the router URL. |
| 5 | `/oauth/authorize` + pending model + NDJSON wait stream + cancel + consent page | B (endpoints) + F (page) | ✅ done | Consent page merged (22 tests) + router endpoints (`internal/oidp/provider.go`, `pending.go`): authorize validation matrix, two-code mint, NDJSON stream matching the pinned §4.2 contract frame-for-frame, cancel, mint rate limit. Live end-to-end visual pass deferred to M6/M7 integration. |
| 6 | Inbound `LoginInterceptor` + Redis Lua claim + publish + bot reactions + STOP | B | ✅ done | Stage-2 interceptor with per-session command cache (`ctrl:oidp.app.changed` invalidation), atomic Lua claim (one winner under `-race`), attempt caps, STOP window, ✅/❌/⌛ feedback. **M9 follow-up:** group mention check requires non-empty mentions + pinned group + membership but can't yet compare against the bot's own JID (self identity not exposed to the inbound hook). |
| 7 | Finalize + `/oauth/token` (PKCE, refresh rotation + reuse-kill) + userinfo + revoke | B | ✅ done | Full token surface + M4's API gaps (grant display fields, `grants:revoke-all`, `issuer` on app DTO). Review fixes by orchestrator: §7.6 claim mapping (group claims acr-gated, `wa_jid` added, internal `wa_identity_id` leak removed — Codex follow-up), injectable-clock JWT validation, embedded auth-code expiry stamp, two fake-clock test fixtures. **M8 follow-up: replace `KEYS`-based lookups in the finalize/cancel Lua scripts with a direct browser-code index (O(N) scan on a hot path).** Off-the-shelf OIDC client e2e still owed (M9). |
| 8 | Grants dashboard + revocation cascades + `ctrl:oidp.*` propagation | F (UI) + B (cascades) | ✅ done | **8-B**: session logout/delete cascade (disable apps → revoke grants + refresh families), `ctrl:oidp.grant.revoked` + router revoked-grant set checked before DB on refresh, wait-stream termination on `ctrl:oidp.app.changed`, and the flagged `KEYS`-scan removed (re-keyed to `oauth2:req:<browser_code>`; spec §3.2/§7.9 updated). **8-F**: grants tab shows `displayName`/`phoneMasked`/`refreshFamilyCount`, server-side `revoke-all`, issuer from app DTO. |
| 9 | Hardening (rate limits, stream caps, phishing copy) + `guides/sign-in-with-whatsapp.md` + spec finalization | B + F | 🔶 impl done, security review running | **9-B**: bot-JID-specific group-mention check (self JID/LID threaded to interceptor), plain-PKCE rejected at token time, stream per-IP(30)/per-code(3) caps + generic 404, full OIDC e2e test (authorize→claim→finalize→token→JWKS-verify→userinfo→replay-fail). **9-F**: `guides/sign-in-with-whatsapp.mdx` integration guide (openid-client walkthrough). Orchestrator fix: injectable clock on `PendingStore` (Load/auth-code expiry used wall clock while authorize stamped a frozen clock — broke the e2e). Independent adversarial §7 review in progress before merge. |

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
- **2026-07-08** — Milestone 4 merged: dashboard OAuth-apps UI (list, create sheet, detail with
  settings/grants/integration tabs, live consent-card preview reusing `ConsentCard`, secret
  copy-once modal, destructive-cascade confirms, workspace nav entry). 48/48 web tests green.
  Backend follow-ups recorded for M7/M8: grant display fields, bulk revoke op, issuer surfacing.
- **2026-07-08** — Milestone 6 landed: gateway stage-2 `LoginInterceptor` (unconditional
  command-namespace interception, per-session cache invalidated by new `ctrl:oidp.app.changed`
  channel published from management CRUD), completed Redis Lua claim (taxonomy: verified /
  already_used / expired / wrong / rate_limited / mode_mismatch / command_mismatch / denied),
  attempt caps + STOP denial window, bot reactions/replies with §7.3 phishing warning. Specs
  (`oauth.md`, `inbound-pipeline.md`) updated in-change. Orchestrator fixed one test that
  asserted against an accumulated reactions slice (hidden in Codex's no-TCP sandbox); full
  gates + `-race` green. Follow-up for M9: expose the bot's own JID/LID to the inbound hook so
  group mode can verify the mention targets the bot specifically.
- **2026-07-08** — Milestone 7 landed: finalize (race-safe verified→finalized, one auth code),
  `/oauth/token` (auth-code + PKCE matrix, client-auth matrix, refresh rotation with family-kill
  reuse detection), `/oauth/userinfo`, `/oauth/revoke`, pairwise subs, plus the M4 management-API
  gaps. Two review rounds: (1) claim-mapping fix — `wa_group_*` claims emitted only for
  `acr=wa:group`, `wa_jid` under `phone`, internal `wa_identity_id` never exposed; (2)
  orchestrator fixes for injectable-clock JWT validation (`jwt.WithClock`), embedded auth-code
  expiry stamp (belt-and-braces vs Redis TTL), a chi route-context test helper, and fake-clock
  test fixtures. Full gateway + web gates and `-race` green. Flagged for M8: `KEYS`-scan in
  finalize/cancel Lua scripts must become a direct index.
- **2026-07-08** — Milestone 8 landed (8-B backend + 8-F UI merged). 8-B: session logout/delete
  cascade through SessionService→OAuthAppService (disable apps, revoke grants + refresh families),
  `ctrl:oidp.grant.revoked` published on revoke/revoke-all/app-delete, router OIDC control-bus
  subscriber (revoked-grant TTL set checked before the authoritative DB check on refresh;
  `ctrl:oidp.app.changed` terminates affected wait-streams), and the hot-path `KEYS` scan removed
  by re-keying pending requests to `oauth2:req:<browser_code>` (spec §3.2/§7.9 updated in-change).
  8-F: grants tab consumes `displayName`/`phoneMasked`/`refreshFamilyCount`, server-side
  `revoke-all`, issuer from the app DTO. Full gateway + web gates and `-race` green.
- **2026-07-08** — Milestone 9 implementation landed (9-B backend + 9-F docs merged). 9-B: group
  mode now requires the bot's own JID/LID in the mention (session self-IDs threaded through the
  normalizer), plain-PKCE rejected at token time, stream per-IP/per-code connection caps + generic
  404 verified, and a full off-the-shelf-OIDC-client e2e test (authorize → claim → finalize →
  token → JWKS verify → userinfo → replay-fail). 9-F: `sign-in-with-whatsapp.mdx` integration
  guide. Orchestrator fix: `PendingStore` got an injectable clock — Load and auth-code expiry
  compared against the wall clock while authorize stamped ExpiresAt from the provider's (frozen,
  in tests) clock, which failed the e2e with `redis: nil`. Full gateway + web gates and `-race`
  green. Independent adversarial security review of §7 (fresh read-only Codex pass) running before
  the branch is proposed for merge.
- **2026-07-08** — Independent adversarial security review of §7 (fresh read-only Codex pass)
  completed. Core protocol **enforced**: auth-code single-use/replay, PKCE S256 (no downgrade),
  pairwise + scoped claims (no internal-id leak), open-redirect exact match, client-secret
  hashing + constant-time compare, org isolation, phishing mitigations, 160-bit browser code.
  Findings triaged: **fix now** (dispatched to Codex) — atomic refresh rotation (TOCTOU, #1 High),
  idempotent finalize (#2 High), atomic user-code mint (#4), user-code TTL non-extension (#5),
  XFF-aware per-IP stream cap (#6), acr_values exact-token match (#8), userinfo `typ==access`
  enforcement, transactional signing-key promotion (#12). **Documented as by-design** (spec §7.9
  / threat matrix updated in-change): app-disable = reversible pause not emergency invalidation
  (use delete/revoke to kill tokens); group membership asserted as-of-cache; management DTO
  exposes the owner's own identity ids. Full review report retained in scratchpad.
