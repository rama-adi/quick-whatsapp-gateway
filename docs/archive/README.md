# Archive — v1 MVP snapshot

This directory preserves the **v1 MVP** design as it stood when the project pivoted to the
v2 split architecture.

## What v1 was

A single self-contained Go binary:

- **Auth/tenancy** embedded via **Authula** (Email&Password, Session, CSRF, TOTP, Access
  Control, Admin, Rate Limit, Secondary Storage on Redis), RBAC (`super_admin`/`user`),
  admin bootstrap from `ADMIN_EMAIL`/`ADMIN_PASSWORD`.
- **WhatsApp** via whatsmeow (session manager, inbound/outbound pipelines, webhooks, NDJSON
  stream).
- **Frontend** = a React Router + shadcn **SPA**, built to static assets and **embedded in
  the Go binary** (`embed.FS`), served on the same port.
- **Storage** = MySQL for app data **and** the whatsmeow keystore (custom MySQL store, SQLite
  fallback); Redis for queue/cache/pubsub/limits.
- Custom account-global **API keys** (argon2id) implemented in Go.

## Status at snapshot

Milestones **M0–M8 code complete** (see `mvp-progress-v1.md`); the only open item was the
manual end-to-end smoke test against a live WhatsApp number. Live Docker-backed boot smoke
passed (45 tables incl. 16 `wmstore_*` keystore tables + Authula tables).

## Why it was superseded

The project split into two independently deployable parts:

- **Gateway (Go)** — whatsmeow-only; no human auth; SQLite keystore; verifies callers.
- **Frontend (TanStack Start)** — fullstack; owns all human identity via **better-auth**
  (replacing Authula) on MySQL; issues JWTs the gateway verifies via **JWKS**.

See the current [`../../masterplan-mvp.md`](../../masterplan-mvp.md) (v2) for the live design.

## Files

| File | What |
|---|---|
| `masterplan-mvp-v1.md` | The full v1 implementation spec (verbatim). |
| `mvp-progress-v1.md` | The v1 milestone tracker at snapshot time. |

## Recovering the v1 code

The implemented v1 backend (`internal/auth/*`, embedded SPA, MySQL keystore, custom API
keys) is tagged in git:

```sh
git show mvp-v1            # the snapshot commit
git checkout mvp-v1        # inspect the full v1 tree
```

> The `mvp-v1` tag is **local** until pushed (`git push origin mvp-v1`).
