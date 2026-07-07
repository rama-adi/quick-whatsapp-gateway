# Sign in with WhatsApp (OAuth 2.1 / OIDC provider) — progress tracker

Design of record: [`docs/specs/oauth.md`](./docs/specs/oauth.md). This file tracks the
implementation milestones (design §11). Each increment lands green (gateway
`go build/vet/test`; web `pnpm build/typecheck/test`) and is committed on its own.

Owners: **B** = gpt-5.5/Codex (backend rigor) · **F** = opus-4.8 (frontend/copy/docs) ·
orchestration + reviews = Fable.

| # | Increment | Owner | Status | Notes |
|---|---|---|---|---|
| 0 | Design synthesis + spec (`docs/specs/oauth.md`) + this tracker | Fable | ✅ done | Opus + gpt-5.5 parallel designs, arbitrated (spec §12). |
| 1 | Migration `0007_oidc_provider` + `internal/store/oauth.go` repos + `pnpm db:introspect` | B | ⬜ pending | Four tables: clients, grants, refresh tokens, signing keys. |
| 2 | Signing keys + `oidp.Signer` + `rotate-key` subcommand + discovery + JWKS | B | ⬜ pending | Dedicated EdDSA keyset, AES-GCM at rest, one JWKS across replicas. |
| 3 | Management CRUD (huma ops) + org isolation + secret hashing + `make gen` | B | ⬜ pending | `/api/v1/oauth-apps*`; secret shown once. |
| 4 | Dashboard OAuth-apps UI | F | ⬜ pending | List / editor (consent-card preview, login-command field) / secret-once modal / grants tab / integration-guide tab. |
| 5 | `/oauth/authorize` + pending model + NDJSON wait stream + cancel + consent page | B (endpoints) + F (page) | ⬜ pending | Two-code model; stream = Pump core + NDJSON Sink; page at `web/app/routes/login.whatsapp.tsx`. |
| 6 | Inbound `LoginInterceptor` + Redis Lua claim + publish + bot reactions + STOP | B | ⬜ pending | Per-app `login_command`; unconditional interception; `-race` tests on claim. |
| 7 | Finalize + `/oauth/token` (PKCE, refresh rotation + reuse-kill) + userinfo + revoke | B | ⬜ pending | Verified end-to-end with an off-the-shelf OIDC client. |
| 8 | Grants dashboard + revocation cascades + `ctrl:oidp.*` propagation | F (UI) + B (cascades) | ⬜ pending | |
| 9 | Hardening (rate limits, stream caps, phishing copy) + `guides/sign-in-with-whatsapp.md` + spec finalization | B + F | ⬜ pending | Final adversarial review of spec §7 before ship. |

## Log

- **2026-07-08** — Design locked and committed (`docs/specs/oauth.md`), tracker created.
  Key arbitrations (spec §12): Redis Lua claim over reverse assertion; auth code minted at
  browser-driven finalize; pairwise subs; dedicated EdDSA signing keys; per-app `login_command`
  with unconditional command-namespace interception.
