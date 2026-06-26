# MVP Progress Tracker

Tracks implementation status against [`masterplan-mvp.md`](../masterplan-mvp.md) §16 milestones.
Last updated: 2026-06-26.

## Milestone status

| Milestone | Status | Notes |
|---|---|---|
| **M0** — Scaffolding | ✅ Done | chi server, config+validate, migrations (app + `wmstore_*`), compose files, Dockerfile (CGO=0), Makefile, embedded SPA shell, `docs/specs/` seeded |
| **M1** — Auth & tenancy | ✅ Done | Authula plugins (Email&Password, Session, CSRF, TOTP, Access Control, Admin, Rate Limit, Secondary Storage), cookie sessions, RBAC, admin bootstrap, `USER_PANEL_ENABLED` |
| **M2** — Sessions | ✅ Done | Session Manager, **MySQL keystore** + SQLite fallback, QR + pairing-code, admin-number bootstrap, status events, reconnect/logout |
| **M3** — Inbound | ✅ Done | normalize+envelope, identity/contacts/groups capture, message/chat/poll-vote persistence, command interceptor (routing stub), auto-read + typing |
| **M4** — Outbound | ✅ Done | unified send + message ops, idempotency, sync + async outbox, per-session rate limiting |
| **M5** — Eventing | ✅ Done | NDJSON stream (`events=*`, heartbeat, cursor resume) + webhooks (HMAC-SHA512, retries, dedup, dead-letter) + catalog |
| **M6** — API & keys | ✅ Done | account-global keys (permissions/rotation), all §11 endpoints wired, OpenAPI 3.1 spec |
| **M7** — Frontend | ✅ Done | React Router + shadcn SPA: auth (login/register/TOTP), admin + user panels, viewer, Contacts, realtime NDJSON stream wiring, QR/pairing UX, key/webhook management. tsc clean, build green, 13/13 vitest, Go embed proven |
| **M8** — Hardening | ✅ Done | AES-GCM at-rest encryption, retention/prune, metrics/health, structured logs, ToS disclaimer. **E2E smoke vs live number: pending** |

## Verified this session

Live Docker-backed boot smoke test against real MySQL (commit `2d94cfb`):
- Server boots end-to-end; admin sign-in returns 200.
- 45 tables created incl. all 16 `wmstore_*` keystore tables + Authula tables.
- `/healthz` `/readyz` `/api/v1/openapi.yaml` → 200; unauthenticated/bogus-bearer → 401.
- Fixed 6 root-cause boot bugs (1 keystore index-size, 4 Authula MySQL-dialect, 1 chi mount prefix). See [`docs/specs/_recon-authula.md`](specs/_recon-authula.md).
- Foundation tests added for `internal/config` + `internal/domain` (§19).

## Intentional v1 stubs (return `501`, per §17)

- Media send/receive (image/video/audio/document/sticker) — parsed, not implemented.
- Channels (create/follow/unfollow/mute) and image status posting.
- Group pending-member approval.
- `amlogin` WhatsApp-as-login command (registry plumbing exists, no-op).

## Remaining work

1. **M8 E2E smoke** — end-to-end test against a real WhatsApp test number (manual; needs a phone).
   This is the only remaining MVP item; all 8 build milestones (M0–M8 code) are complete.

## Known risks / follow-ups

- The Authula MySQL driver shim ([`internal/auth/mysqldriver.go`](../internal/auth/mysqldriver.go)) is a
  string-rewriting workaround for upstream dialect bugs; revisit on any Authula version bump.
