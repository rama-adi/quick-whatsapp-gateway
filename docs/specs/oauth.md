# Sign in with WhatsApp — OAuth 2.1 / OIDC provider (`internal/oidp`)

Status: **in progress** (design locked; implementation tracked in
[`../../oauth2-progress.md`](../../oauth2-progress.md)).
Owner track: router (Go) + gateway inbound hook + frontend (web/).

## Purpose

Turn a WhatsApp session an org already owns into an **OpenID Connect identity provider**. An org
registers an **OAuth application** bound to one of its sessions; third-party ("relying") apps then
implement *Sign in with WhatsApp* against us as a standard OAuth 2.1 / OIDC authorization server.

The novelty is the authentication factor: instead of a password page, the end-user **proves control
of a WhatsApp number** by sending a short human-typeable code (`login 483920`) as a DM to the app's
bot number, or by **mentioning the bot with that code inside a pinned group** (which additionally
proves group membership). Everything else is boring, standard, RFC-shaped OAuth/OIDC — which is the
point: relying apps drop in any off-the-shelf OIDC client library.

Package name **`oidp`** (OpenID Provider) — distinct from `authz` (caller identity) and
`assertion` (router→gateway trust), which are unrelated trust seams.

## Protocol profile

Supported: OAuth 2.1 authorization-code flow only; **PKCE S256 mandatory for all clients**
(confidential included, `plain` rejected); exact `redirect_uri` matching; OIDC discovery, JWKS,
id_token, UserInfo; refresh-token rotation with persistent grants and family-kill reuse detection;
RFC 7009 revocation; public and confidential clients; **pairwise subject identifiers**.

Not supported: implicit, ROPC, client-credentials for end-user identity, dynamic client
registration (RFC 7591 — apps are created in the dashboard only), RFC 8628 device grant as a
client-facing grant (we borrow its *phishing mitigations*, not its endpoint).

---

## 1. Concepts

| Term | Meaning |
|---|---|
| **OAuth application** (`client`) | Org-owned registration: `client_id`, optional secret (confidential), redirect URIs, verification modes, bound to **exactly one WhatsApp session** (the "bot"). |
| **Relying app** | The third party integrating *Sign in with WhatsApp*. |
| **End-user** | The WhatsApp user logging in; not a better-auth user. |
| **Long browser code** (`browser_code`) | High-entropy (160-bit) URL-safe string. Lives in the consent-page URL **fragment**, keys the wait stream + finalize. **Never** shown to WhatsApp. |
| **Short user code** (`user_code`) | The 6-digit code typed into WhatsApp (`login 483920`). Never a standalone key to OAuth data. |
| **Pending auth request** | The stashed `/authorize` request in Redis, awaiting WhatsApp verification. |
| **Grant** | Persistent (client, WhatsApp identity) consent record in MySQL; parent of refresh tokens. |
| **Verification mode** | `dm` (prove number control) and/or `group` (additionally prove membership in the pinned group). Per-app config; chosen per request via `acr_values` when both are enabled. |

## 2. End-to-end flow

1. Relying app redirects the browser to `GET /oauth/authorize` (`response_type=code`, `client_id`,
   exact `redirect_uri`, `scope`, `state`, `code_challenge` + `S256`, optional `nonce`,
   `acr_values`).
2. Router validates client/redirect/PKCE/scopes, resolves the verification mode, mints the
   **browser_code** (160-bit) + **user_code** (6-digit, crypto-random, collision-checked and
   pattern-filtered within the session), stashes the pending request in Redis, and `302`s to
   `{WEB_URL}/login/whatsapp#c=<browser_code>` (fragment — never in logs/Referer).
3. The consent/waiting page reads the fragment, opens
   **`GET /oauth/wait/{browser_code}/stream`** — a **long-lived HTTP GET streaming NDJSON**
   (fetch + `ReadableStream`; same transport as the gateway event stream; not WebSocket, not SSE).
   First line carries `{status:"pending", app:{name,logo}, user_code, target, scopes, expires_at}`;
   the page renders "Send `login 483920` to +62xxx" (with a `wa.me` deep-link/QR pre-filling the
   DM) or the group-mention instruction, plus a countdown and a "This isn't me / cancel" action.
4. The end-user sends the message. The owning gateway's inbound pipeline (stage-2 interceptor)
   matches it, validates mode/group semantics, and **atomically claims the pending request in
   Redis (Lua)**, attaching the sender's identity. It publishes `verified` on the flow's pub/sub
   channel and the bot replies/reacts to confirm (named app, ✅ / ❌ / ⌛).
5. The stream emits `verified`; the page calls **`POST /oauth/wait/{browser_code}/finalize`**; the
   router upserts the grant, mints a **single-use authorization code** (Redis, hashed key, 60s),
   and returns the full `redirect_uri?code=…&state=…`; the browser navigates there.
6. Relying app exchanges the code at `POST /oauth/token` (PKCE verified) → `id_token`,
   `access_token`, and (with `offline_access`) a rotating `refresh_token`. `GET /oauth/userinfo`
   serves claims for the access token.

The **auth code is minted at finalize, not at verification** — it never transits pub/sub or the
stream, and issuance is bound to the holder of the browser code at that moment.

## 3. Data model

### 3.1 MySQL (gateway golang-migrate; migration `migrations/0007_oidc_provider.{up,down}.sql`)

Four tables, all matching `store.md` conventions (org-keyed, epoch-ms BIGINT timestamps, ULID
`VARCHAR(64)` ids, plain-SQL repos in `internal/store/oauth.go`).

```sql
-- The org-owned OAuth application (client). Bound to one WA session.
CREATE TABLE oauth_clients (
  id                  VARCHAR(64) PRIMARY KEY,          -- ULID; internal PK
  client_id           VARCHAR(64) NOT NULL,             -- public identifier (distinct from id)
  organization_id     VARCHAR(64) NOT NULL,
  created_by_user_id  VARCHAR(64) NULL,                 -- audit
  session_id          VARCHAR(64) NOT NULL,             -- the bot session (→ wa_sessions.id, same org)
  name                VARCHAR(255) NOT NULL,            -- shown on consent page + bot confirmation
  logo_url            TEXT NULL,
  client_type         ENUM('confidential','public') NOT NULL DEFAULT 'confidential',
  login_command       VARCHAR(32) NOT NULL DEFAULT 'login', -- the keyword end-users type (per-app, e.g. "masuk")
  secret_hash         VARBINARY(64) NULL,               -- SHA-256(secret + pepper); NULL for public
  secret_last4        VARCHAR(8) NULL,
  redirect_uris       JSON NOT NULL,                    -- exact-match set; https only (localhost http for dev); no fragments
  modes               SET('dm','group') NOT NULL DEFAULT 'dm',
  group_jid           VARCHAR(255) NULL,                -- pinned group (required iff 'group' in modes)
  allowed_scopes      JSON NOT NULL,
  token_ttl_seconds   INT NOT NULL DEFAULT 900,         -- access/id_token lifetime (15 min default)
  refresh_ttl_seconds INT NOT NULL DEFAULT 2592000,     -- 30 d
  status              ENUM('active','disabled') NOT NULL DEFAULT 'active',
  created_at          BIGINT NOT NULL,
  updated_at          BIGINT NOT NULL,
  deleted_at          BIGINT NULL,                      -- soft delete for audit
  UNIQUE KEY uq_oauth_client_id (client_id),
  KEY idx_oauth_clients_org (organization_id),
  KEY idx_oauth_clients_session (session_id)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- Persistent consent: one row per (client, WhatsApp identity). Parent of refresh tokens.
CREATE TABLE oauth_grants (
  id               VARCHAR(64) PRIMARY KEY,
  organization_id  VARCHAR(64) NOT NULL,
  client_id        VARCHAR(64) NOT NULL,                -- → oauth_clients.client_id
  wa_identity_id   BIGINT UNSIGNED NOT NULL,            -- → whatsapp_identities.id
  sub              VARCHAR(80) NOT NULL,                -- pairwise subject issued to this client
  granted_scopes   JSON NOT NULL,
  last_acr         VARCHAR(16) NOT NULL,                -- 'wa:dm' | 'wa:group' (last login)
  last_group_jid   VARCHAR(255) NULL,
  created_at       BIGINT NOT NULL,
  last_used_at     BIGINT NOT NULL,
  revoked_at       BIGINT NULL,                         -- NULL = active
  UNIQUE KEY uq_grant_client_identity (client_id, wa_identity_id),
  KEY idx_grants_org_client (organization_id, client_id),
  KEY idx_grants_sub (sub)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- Refresh tokens: one row per issued token; rotation chains + reuse detection.
CREATE TABLE oauth_refresh_tokens (
  id               VARCHAR(64) PRIMARY KEY,
  grant_id         VARCHAR(64) NOT NULL,                -- → oauth_grants.id
  organization_id  VARCHAR(64) NOT NULL,
  token_hash       VARBINARY(64) NOT NULL,              -- SHA-256 of the opaque token
  family_id        VARCHAR(64) NOT NULL,                -- reuse anywhere kills the family
  parent_id        VARCHAR(64) NULL,
  scopes           JSON NOT NULL,
  issued_at        BIGINT NOT NULL,
  expires_at       BIGINT NOT NULL,                     -- absolute; rotation does not extend past family max
  consumed_at      BIGINT NULL,                         -- set when rotated; second use = reuse attack
  revoked_at       BIGINT NULL,
  UNIQUE KEY uq_refresh_hash (token_hash),
  KEY idx_refresh_grant (grant_id),
  KEY idx_refresh_family (family_id),
  KEY idx_refresh_org (organization_id)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- OIDC signing keys (shared by all router replicas → one published JWKS).
CREATE TABLE oauth_signing_keys (
  kid          VARCHAR(64) PRIMARY KEY,
  alg          VARCHAR(16) NOT NULL DEFAULT 'EdDSA',
  public_jwk   JSON NOT NULL,
  private_enc  VARBINARY(4096) NOT NULL,                -- AES-GCM encrypted with OIDC_KEY_ENC_KEY
  status       ENUM('active','next','retired') NOT NULL,-- exactly one 'active'
  created_at   BIGINT NOT NULL,
  retired_at   BIGINT NULL
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
```

MySQL holds **durable identity state** (clients, grants, refresh tokens, keys — dashboard-listable,
revocable, cascade-aware). Redis holds everything ephemeral.

### 3.2 Redis (work Redis, `{REDIS_PREFIX}:` namespace, shown without prefix)

```
oauth2:req:<browser_code>                   JSON  pending auth request           TTL 600s
oauth2:usercode:<session_id>:<user_code>    STR   "<browser_code>"               TTL 600s (reverse index)
oauth2:authcode:<sha256(code)>              JSON  authorization code payload     TTL 60s
oauth2:rl:mint:<session_id>                 ctr   pending-code mint rate         sliding
oauth2:rl:verify:<sender_lid>               ctr   per-sender wrong-code attempts sliding 300s
```

Pending-request JSON: authorize params (client_id, redirect_uri, state, nonce, code_challenge(+method),
scopes), `session_id`, resolved `mode`, `user_code`, `login_command` (snapshot of the app's
command at authorize time — what the consent page displays and the claim validates), `status`
(`pending | verified | finalized | denied | expired`), `attempts`, and — once claimed — the
`verified` block `{lid, phone_jid, phone_number, push_name, group_jid?, verified_at}`.

**Pub/sub channel** (un-prefixed like `evt:`): `oauth2:login:<browser_code>` — carries only status
transitions (`verified | denied | expired`), **never tokens or auth codes**. Unguessable because it
is keyed by the 160-bit browser code.

**Atomicity.** The gateway's claim runs as a single Lua script: resolve reverse index → load
request → assert `status == pending`, session + mode match → on wrong-code, bump attempt counters
(fail-closed at 10 per request) → on success set `status = verified` + `verified` block and
`DEL` the reverse index. First claim wins; duplicate WhatsApp deliveries, reconnect replays, and
any future session re-homing all collapse to one binding. Authorization codes are redeemed with
`GETDEL` (single-use); finalize flips `verified → finalized` with a guarded transition so router
replicas mint exactly one code.

### 3.3 State machines

```
pending request:  pending → verified → finalized        auth code:  exists → redeemed (GETDEL) | expired
                  pending → denied | expired            refresh:    active → used → (reuse ⇒ family revoked)
                  verified → expired                                active → revoked | expired
                                                        grant:      active → revoked
```

## 4. Endpoints

All OIDC-facing endpoints are **router-local** (hand-mounted in `internal/router`, like
`/.well-known/*` — not proxied, not huma-registered since they follow RFC shapes). The management
CRUD is huma-registered inside the authenticated `/api/v1` group.

### 4.1 Discovery & keys (public, cacheable)

| Method | Path | Purpose |
|---|---|---|
| GET | `/.well-known/openid-configuration` | OIDC discovery. `issuer` = `OIDC_ISSUER` (default `ROUTER_PUBLIC_URL`). |
| GET | `/.well-known/oauth-authorization-server` | RFC 8414 alias. |
| GET | `/.well-known/oauth-jwks.json` | OIDC signing JWKS (`active` + `next`). **Separate** from `router-jwks.json` (internal assertions). |

Discovery advertises: `response_types_supported: ["code"]`, `grant_types: ["authorization_code",
"refresh_token"]`, `code_challenge_methods: ["S256"]`, `id_token_signing_alg: ["EdDSA"]`,
`subject_types: ["pairwise"]`, `acr_values_supported: ["wa:dm","wa:group"]`,
`scopes_supported: ["openid","profile","phone","wa:group","offline_access"]`.

### 4.2 Protocol endpoints

| Method | Path | Auth | Purpose |
|---|---|---|---|
| GET | `/oauth/authorize` | none (validates `client_id` + exact `redirect_uri` first) | Validate, mint codes, stash, `302` to consent page. |
| GET | `/oauth/wait/{browser_code}/stream` | browser_code capability | **NDJSON HTTP stream**: current status snapshot on connect, then tail (`verified`/`denied`/`expired`), heartbeat ~20s, hard-close at request expiry. |
| POST | `/oauth/wait/{browser_code}/finalize` | browser_code capability | `verified → finalized`; upsert grant, mint auth code, return `{redirect: "<redirect_uri>?code=…&state=…"}`. |
| POST | `/oauth/wait/{browser_code}/cancel` | browser_code capability | "This isn't me" → `denied`, reverse index deleted, `denied` published. |
| POST | `/oauth/token` | client auth (Basic or form for confidential; `client_id`+PKCE for public) | Code exchange + refresh rotation. |
| GET/POST | `/oauth/userinfo` | Bearer access token | OIDC claims, read fresh from `whatsapp_identities`. |
| POST | `/oauth/revoke` | client auth | RFC 7009 (refresh or access). |

`/oauth/wait/*` never returns tokens, secrets, or the auth code before finalize; unknown/expired
codes get a generic `404`. Error taxonomy: invalid `client_id`/`redirect_uri` → local error page,
**never** a redirect; other authorize errors → redirect with `error` + `state`; token endpoint
returns standard OAuth JSON errors with no internal detail.

**Pinned wire contract** (the consent page is implemented against exactly this; the backend must
honor it):

- Stream frames are one JSON object per line. First frame is the snapshot:
  `{status:"pending", app:{name, logo}, user_code, login_command, target, scopes, expires_at}` —
  `logo` nullable, `scopes` a string array, **`expires_at` epoch milliseconds**.
- `target` = `{mode:"dm", number, bot_name?}` or `{mode:"group", group_name, number?, bot_name?}`;
  `number` human-readable (the page strips to digits for the `wa.me` link).
- Subsequent frames: `{status:"heartbeat"}` (~20s liveness) and terminal
  `{status:"verified"|"denied"|"expired"}`. A reconnect to the same stream URL re-emits the
  current snapshot first (idempotent).
- Stream `404` = unknown/expired code (terminal for the page); any other non-2xx or dropped body
  is transient → the page reconnects with capped backoff.
- `POST …/finalize` → `200 {redirect:"<redirect_uri>?code=…&state=…"}`; `POST …/cancel` → any 2xx
  (body ignored). Both cookie-less, no request body.

### 4.3 Mode selection — `acr_values`

- `acr_values=wa:dm` → DM verification; `acr_values=wa:group` → group verification.
- Omitted: default to `dm` if enabled, else `group`.
- Requested acr not in the app's `modes` → `invalid_request`.
- The `acr` actually met is echoed in the id_token, so a relying app can *require*
  `acr == "wa:group"` before trusting membership.

### 4.4 Management CRUD (huma, org-scoped: `internal/http/handlers/oauth_app_ops.go` + `internal/apitypes/oauth.go`)

| OperationID | Method | Path | Cap |
|---|---|---|---|
| `listOAuthApps` | GET | `/api/v1/oauth-apps` | Read |
| `createOAuthApp` | POST | `/api/v1/oauth-apps` | Manage — returns `client_secret` **once** |
| `getOAuthApp` | GET | `/api/v1/oauth-apps/{id}` | Read |
| `updateOAuthApp` | PATCH | `/api/v1/oauth-apps/{id}` | Manage |
| `rotateOAuthAppSecret` | POST | `/api/v1/oauth-apps/{id}:rotate-secret` | Manage — shown once, old invalid immediately |
| `deleteOAuthApp` | DELETE | `/api/v1/oauth-apps/{id}` | Manage — cascade-revokes grants + tokens |
| `enableOAuthApp` / `disableOAuthApp` | POST | `/api/v1/oauth-apps/{id}:enable` / `:disable` | Manage |
| `listOAuthAppGrants` | GET | `/api/v1/oauth-apps/{id}/grants` | Read |
| `revokeOAuthAppGrant` | DELETE | `/api/v1/oauth-apps/{id}/grants/{grantId}` | Manage |

Org isolation identical to sessions: `{id}` in another org → `404` (super_admin bypass);
`session_id` on create/update must resolve to the caller's org.

## 5. The inbound verification hook

### 5.1 Placement & matching

A new `oidp.LoginInterceptor` at **stage 2 (command interceptor)** of `internal/wa/inbound` — runs
on any session that owns ≥1 active OAuth app (cheap cached set, invalidated via
`ctrl:oidp.app.changed`), and matched messages are **never persisted or fanned out**.

Eligibility: `KindMessage`, `FromMe == false`, body matches
`^\s*(<cmd1>|<cmd2>|…)\s+(\d{6})\s*$` (case-insensitive), where the alternation is the set of
**login commands of the session's active OAuth apps** (per-app `login_command`, default `login`;
cached per session, invalidated via control bus on app create/update/disable).

**Interception is unconditional on shape match**: any message matching an active command pattern
on that session is consumed at stage 2 — never persisted, never fanned out to webhooks or the
event stream — *even when the code is wrong or expired*. This is deliberate: login traffic (and
brute-force noise) must not collide with or leak into the customer's own message handling; we
intercept the command namespace for them. Command validation at create/update: single word,
`[a-z0-9_-]{2,32}`, case-insensitive match, must not collide with the session's admin command
prefix (`WHATSAPP_ADMIN_CMD_PREFIX`).

Context rules:

- **DM mode**: `nm.IsDM == true`. A `dm`-mode request must arrive as a DM.
- **Group mode**: `nm.IsGroup == true`, `nm.ChatJID == client.group_jid`, and the bot's JID/LID
  present in `nm.Mentions`. Sender identity comes from event metadata (never parsed from body).
  The message arriving *in* the pinned group is the membership proof at send time; if the group
  member cache says the sender already left, reject.

### 5.2 Verification transport — Redis atomic claim (decision)

The gateway claims the pending request **directly in Redis via the Lua script** (§3.2), passing
the observed command word — the claim additionally asserts it equals the pending request's
`login_command` snapshot, so app B's command can't redeem app A's code — and publishes `verified`
on `oauth2:login:<browser_code>`. **No gateway→router HTTP call and no new
reverse assertion key.**

Arbitration note (the two designs disagreed): a reverse gateway→router Ed25519 seam would add a
second keypair, JWKS, and verifier for one internal call. The Lua claim gives the gateway the same
synchronous answer (claimed / already-used / expired / wrong-mode → ✅ / ⌛ / ❌ reactions), Redis is
already shared trusted infra between the two services, and **durable** state stays router-only:
the gateway mutates only the ephemeral pending request; grants, auth codes, and tokens are minted
exclusively by the router at finalize/token time. Fewer moving parts, same guarantees; router
restarts don't lose verified state because Redis holds it.

Claim result taxonomy: `verified`, `already_used`, `expired`, `wrong`, `rate_limited`,
`mode_mismatch`, `command_mismatch`, `denied`. Bot feedback: on claim success → react ✅ +
*"You're signed in to **<App name>**. Return to your browser."* plus the §7.3 warning naming the
app and a `STOP` hint; unknown/expired/wrong/mismatch → ❌ + generic "invalid or expired"
(rate-limited, no oracle); already-used → ⌛; rate-limited → silent. A **`STOP`** reply within the
window flips the request to `denied` (§7.3).

### 5.3 Races / multi-gateway

Sessions are pinned to one gateway, so one gateway observes a given login message — but the Lua
claim is still the authority: duplicate deliveries, reconnect replays, and future re-homing all
resolve to first-claim-wins. Router replicas racing on finalize are serialized by the guarded
`verified → finalized` transition; exactly one auth code is ever minted per flow.

## 6. Consent / waiting page & dashboard (web/)

### 6.1 Consent page — public route `web/app/routes/login.whatsapp.tsx`

No `beforeLoad` auth. Browser code arrives in the **URL fragment** (`#c=…`) so it never appears in
web-server logs or `Referer`. `Cache-Control: no-store`, `Referrer-Policy: no-referrer`, no
third-party scripts.

1. Read fragment → open the NDJSON stream with `fetch` + `ReadableStream` (line-buffered reader).
2. Render from the `pending` snapshot: app name + logo, requested scopes in plain language, the
   big monospace `<login_command> 483920` (the app's configured command, e.g. `masuk 483920`) with
   copy button, a `wa.me` deep-link/QR pre-filling the DM
   (`https://wa.me/62812xxx?text=masuk%20483920`), mode-specific instructions, expiry countdown,
   and the warning *"Only continue if you started this sign-in yourself."*
3. On `verified` → `POST …/finalize` → `window.location.replace(redirect)`. On
   `denied`/`expired` → terminal state (no retry with the same code). Cancel button → `POST
   …/cancel`.
4. Stream drop before a terminal state → reopen the same URL; the server re-emits the current
   snapshot, so reconnects are idempotent.
5. **A page reload kills the attempt.** The page stamps a per-page-load id into
   `sessionStorage` keyed by the browser code; loading the page again for a code this tab
   already owned (refresh, back-nav) → `POST …/cancel` + a "cancelled because the page was
   reloaded" terminal screen, instead of resuming. Driver-internal reconnects (step 4) are the
   same page load and are unaffected; blocked storage fails open (resume, never false-kill).
   After the flow completed (`finalized`) or expired, the code is spent — a reconnect gets the
   `finalized` frame (or a 404) and renders the invalid-link terminal state; nothing resumes.

Consent semantics: **the WhatsApp message is the consent** — the user actively proves identity to
a named, branded app. No separate Allow/Deny button; the identity display + cancel/STOP is the
phishing guard.

### 6.2 Dashboard — `web/app/routes/dashboard.oauth-apps.tsx` (+ detail)

- **List**: name, logo, bound session (number + status pill), mode chips, grant count, status.
- **Create/edit**: name + logo (with a **live preview of the consent card**), **login command**
  (single word, default `login`, validated `[a-z0-9_-]{2,32}`; the preview and the note "messages
  starting with this word on the bound session are intercepted and won't reach your webhooks"
  update live), session picker (org's working sessions), redirect-URI multi-input (absolute https, exact; localhost http for
  dev; fragments rejected), DM/Group toggles (Group reveals a group picker from the session's
  groups), scope checklist with plain-language descriptions, advanced TTLs (clamped), client type.
- **Secret UX**: shown exactly once on create/rotate (copy-once modal, matches the api-key UX);
  thereafter last4 + created-at only.
- **Grants tab**: WhatsApp display name, masked phone, scopes, `acr`, first/last login, active
  refresh-family count, per-row Revoke + "revoke all".
- **Integration guide tab**: generated quickstart for the relying-app developer — discovery URL,
  `client_id`, filled-in authorize URL, and a runnable `openid-client` (Node) snippet. Should read
  like a Stripe/Auth0 quickstart.

## 7. Token, key & security design

### 7.1 Why the two-code split works

The browser code (160-bit, fragment) keys completion; the user code (6-digit) is only a
session-scoped, single-use lookup. **Observing the 6-digit code cannot hijack a flow** — an
attacker without the browser code can't watch the stream, can't finalize, and the redirect goes
only to the app's registered URI. The worst misuse of a stolen user code is consuming it with the
attacker's own WhatsApp identity — which fails safe.

### 7.2 Brute force of the user code

- Codes are crypto-random 6-digit, collision-checked per session, patterned values rejected
  (`000000`, `123456`, repeats); live ≤600s, with an absolute expiry that wrong attempts and
  successful claims never extend.
- **Per-sender cap**: 5 wrong attempts / 300s per WhatsApp identity → silent drop (no reaction, no
  oracle). Each guess costs the attacker a real WhatsApp message from a real number — WhatsApp
  itself throttles volume.
- **Per-request cap**: 10 wrong attempts → request fails closed.
- **Mint rate limit** per session so an attacker can't inflate the live-code count via `/authorize`
  spam. Success probability per guess ≈ (live codes)/10⁶.
- Escape hatch if abuse appears: per-app 8-digit toggle.

### 7.3 Code-relay phishing (RFC 8628 §5.4 class — the real threat)

Attack: attacker starts a login, gets `483920`, social-engineers the victim into sending it — the
victim's identity would be bound to the attacker's browser. Mitigations, layered: the bot's reply
**names the app and warns** (*"⚠️ Signing in to **Acme**. If you didn't start this on Acme's site,
reply STOP."*); `STOP` aborts the flow; 600s TTL forces near-real-time relay; both the page and the
chat show the same app identity (mismatch is the tell); group mode shrinks the victim pool to group
members and adds social visibility; relying-app docs mandate showing their own name on the button.
**Residual risk is documented honestly**: this factor proves number control, not intent — we do not
market it as phishing-resistant, and money/PII apps should layer a second factor.

### 7.4 Signing keys

Dedicated EdDSA/Ed25519 keypair(s) in `oauth_signing_keys` — **not** the router-assertion key
(different rotation cadence, public federation vs internal seam) and not env-vars (replicas must
share one JWKS). Rotation: pre-publish `next` → promote → keep `retired` in JWKS until its tokens
expire. Private key AES-GCM-encrypted at rest (`OIDC_KEY_ENC_KEY`). Minting/rotation via a
`cmd/router oidp rotate-key` subcommand.

### 7.5 Subjects — pairwise

`sub = base64url(HMAC-SHA256(OIDC_PAIRWISE_SALT, "v1:" + client_id + ":" + wa_identity_id))` —
stable across renames (derived from the lid-keyed identity row, matching the repo's read-time
resolution principle), unlinkable across clients. Apps that want a correlatable identifier request
the `phone` scope explicitly.

### 7.6 Scopes & claims

| Scope | Claims granted |
|---|---|
| `openid` | `sub`, `acr`, `amr:["whatsapp"]`, `auth_time` (required) |
| `profile` | `name` (push/business name, resolved read-time from `whatsapp_identities`) |
| `phone` | `phone_number` (E.164), `phone_number_verified: true`, `wa_jid` |
| `wa:group` | `wa_group_verified`, `wa_group_id`, `wa_group_name` (group mode only) |
| `offline_access` | refresh token issuance |

Standard OIDC claim names verbatim so client libraries map them for free; provider-specific claims
namespaced `wa_*`.

### 7.7 Token formats & lifetimes

- **id_token**: EdDSA JWT; `iss`, `aud=client_id`, `sub`, `nonce` echo, `acr`, `amr`, `auth_time`;
  TTL = app `token_ttl_seconds` (default 900).
- **access_token**: EdDSA JWT (self-contained; `/userinfo` and relying-party resource servers
  verify offline via JWKS — no hot-path DB hit); claims `scope`, `client_id`, `jti`; same TTL. Not
  persisted; revocation is short-TTL + grant/refresh revocation. `/oauth/userinfo` only accepts
  tokens with `typ:"access"`; presenting an `id_token` is rejected.
- **refresh_token**: opaque 256-bit random, SHA-256 hash at rest; TTL = app `refresh_ttl_seconds`
  (default 30d, absolute family max); **rotation mandatory**, reuse of a consumed token revokes
  the whole family (`SELECT … FOR UPDATE` on the family for atomic swap).
- **authorization code**: opaque 256-bit random, Redis-keyed by its SHA-256, 60s, single-use
  `GETDEL`, bound to `client_id` + exact `redirect_uri` + PKCE challenge (all re-checked at
  `/token`).

### 7.8 Threat matrix (summary)

| Threat | Mitigation |
|---|---|
| User-code brute force | §7.2 caps + tiny window + session-scoped index + no oracle |
| Code-relay phishing | §7.3 branded confirmation + STOP + TTL + documented residual |
| Stream enumeration/DoS | 160-bit code; generic 404; per-IP + per-code concurrent-connection caps; heartbeat; hard-close at expiry; `X-Forwarded-For` is trusted only when `OIDC_TRUST_PROXY=true` |
| Auth-code replay/injection | GETDEL single-use, 60s, PKCE S256, client+redirect binding |
| Open redirect | Exact-match set, no wildcards/fragments; invalid client/redirect never redirects |
| Mix-up | Single issuer; `iss` in id_token + RFC 9207 `iss` on the redirect |
| PKCE downgrade | S256 only; required for confidential too |
| Group-membership spoofing | Message must arrive in the pinned group with a mention of the session's own bot JID/LID; sender from event metadata; member-cache cross-check. Membership is asserted **as of the stored `whatsapp_group_members` cache** (refreshed by group traffic + participant events), not a live participant fetch — a member removed since the last observed event may linger briefly. `wa_group_verified` therefore means "member per cache at auth time"; apps needing continuous enforcement use short `token_ttl` and re-auth. |
| Refresh-token theft | Rotation + family-kill; hashed at rest; dashboard revoke |
| DB dump | Signing keys encrypted at rest; secrets/tokens hashed |
| Secret leak | SHA-256+pepper at rest, shown once, rotate op; public clients have no secret (PKCE only) |

### 7.9 Cascades & revocation

Reuses the **control bus**: `ctrl:oidp.app.changed` invalidates each gateway's per-session active
login-command cache on app create/update/enable/disable/delete; later grant/token cascades use
`ctrl:oidp.grant.revoked`.

- **App deleted** → soft-delete; revoke all grants + refresh families; publish
  `ctrl:oidp.app.changed` + `ctrl:oidp.grant.revoked`; open wait-streams for that client are
  denied on the router's app-change handling; outstanding codes/tokens refused.
- **App disabled** → **pause semantics**: new authorizations and token grants (incl. refresh) are
  refused while disabled, because the token endpoint authenticates the client via
  `GetActiveByClientID`. Grants and refresh tokens are **retained**, so re-enabling restores them —
  disable is a reversible pause, **not** emergency invalidation. To permanently kill access
  (e.g. a leaked secret or stolen refresh token), use **delete** or **revoke grants**, which
  revoke the refresh families. (The dashboard's disable action states this.)
- **Bound session logged out / deleted** → apps auto-disabled, grants + refresh tokens revoked,
  `ctrl:oidp.app.changed` + `ctrl:oidp.grant.revoked` published (dashboard confirms: "N apps use
  this session"). Transient session stop → authorize returns `temporarily_unavailable`; existing
  tokens unaffected.
- **Grant revoked** (dashboard) → all its refresh tokens revoked; `ctrl:oidp.grant.revoked`
  published so router replicas reject refresh immediately alongside the authoritative DB check;
  access JWTs die by TTL.
- **Org disabled** → orphan-guard already stops sessions; clients treated disabled; refresh
  refused.

## 8. Observability

Structured events: `oauth_authorize_created`, `oauth_wait_connected`, `oauth_login_attempt`,
`oauth_login_verified`, `oauth_login_failed`, `oauth_code_redeemed`, `oauth_refresh_rotated`,
`oauth_refresh_reuse_detected`, `oauth_grant_revoked`. Metrics on authorize/verify/token outcomes
by client + mode, pending expiries, wrong-code attempts, stream connection counts, reuse
detections. Management actions audit-logged with org/client/actor.

## 9. Composition root

`cmd/router`: Redis client (shared with realtime), the four `internal/store` OAuth repos, an
`oidp.Provider` (code minters, `Signer` + JWKS cache, token issuer, wait-stream handler = `Pump`
core + NDJSON `Sink`, finalize/cancel handlers), control-bus subscriptions for `ctrl:oidp.*`.

`cmd/server` (gateway): `oidp.LoginInterceptor` in inbound stage 2, constructed with the cached
active-app set per session, the Redis client (Lua claim + publish), and the outbound handler for
bot reactions/replies.

## 10. Bookkeeping plan

- **NEW `docs/specs/oauth.md`** (this document, trimmed to the spec template) + index row in
  `_V2-STATUS.md` + milestone track & locked decisions in `docs/mvp-progress.md`.
- **Update** `router.md` (routes), `inbound-pipeline.md` (stage-2 interceptor),
  `store.md` (four tables), `trust-model.md` (`ctrl:oidp.*`), `queue.md` (`oauth2:*` keys/channels),
  `stream.md` (NDJSON `Sink` reuse), `frontend.md` (consent page + dashboard routes).
- **OpenAPI**: management CRUD via huma → `make gen` (openapi + typed client + fumadocs API ref).
  Protocol endpoints are RFC-shaped, hand-mounted, documented in the spec + guide (not huma).
- **Migration** `0007_oidc_provider` → `make migrate` → `cd web && pnpm db:introspect`.
- **NEW guide** `web/content/docs/guides/sign-in-with-whatsapp.md` (relying-app integration
  quickstart).
- **Env** (`deploy/.env.example`): `OIDC_ISSUER` (default `ROUTER_PUBLIC_URL`),
  `OIDC_KEY_ENC_KEY`, `OIDC_PAIRWISE_SALT`, `OAUTH_CLIENT_SECRET_PEPPER`, `WEB_LOGIN_URL`
  (default `${WEB_URL}/login/whatsapp`), TTL overrides (`OIDC_REQUEST_TTL_SECONDS=600`,
  `OIDC_AUTHCODE_TTL_SECONDS=60`).

## 11. Milestone breakdown

Each increment independently green (gateway `go build/vet/test`; web `build/typecheck/test`).
**B** = high-rigor backend engineer (gpt-5.5), **F** = high-taste frontend/docs engineer
(opus-4.8); reviews cross the seam.

| # | Increment | Owner | Green deliverable |
|---|---|---|---|
| 1 | Migration `0007` + `internal/store/oauth.go` repos + `db:introspect` | B | Tables migrate up/down; repos unit-tested |
| 2 | Signing keys + `oidp.Signer` + rotate subcommand + discovery + JWKS | B | Signed JWT verifies against published JWKS |
| 3 | Management CRUD (huma) + org isolation + secret hashing + `make gen` | B | Contract tests pass; typed client regenerated |
| 4 | Dashboard OAuth-apps UI (list/editor/consent-preview/secret-once/guide tab) | F | Owner creates an app end-to-end against real API |
| 5 | `/oauth/authorize` + pending model + NDJSON wait stream + cancel + consent page | B (endpoints) + F (page) | `/authorize` → page shows code, streams `pending`, heartbeats, expires |
| 6 | Inbound `LoginInterceptor` + Lua claim + publish + bot reactions + STOP | B | Live login: DM fires the stream; race/single-use tests (`-race`) |
| 7 | Finalize + `/oauth/token` (PKCE, rotation + reuse-kill) + userinfo + revoke | B | Off-the-shelf OIDC client completes sign-in against a running stack |
| 8 | Grants dashboard + cascades + `ctrl:oidp.*` propagation | F (UI) + B (cascades) | Revocation propagates, streams drop |
| 9 | Hardening (rate limits, per-IP stream caps, phishing copy) + docs + spec finalization | B (limits) + F (copy/docs) | Threat-model mitigations tested; guide live |

High-stakes checkpoints to run **gpt-5.5 and a second reviewer in parallel** on: increment 6
(claim atomicity / single-use) and increment 7 (token endpoint correctness), plus a final
adversarial review of §7 before ship.

## 12. Synthesis arbitration log

Where the two source designs disagreed, this document picks:

1. **Verification transport**: Redis atomic Lua claim + pub/sub (gpt-5.5) over a new
   gateway→router reverse-assertion HTTP seam (opus) — same synchronous guarantees, no second
   keypair/JWKS, durable writes stay router-only.
2. **Auth-code minting**: at browser-driven `finalize` (gpt-5.5) — the code never transits
   pub/sub — combined with opus's URL-fragment browser code and richer stream payload.
3. **Naming/taste**: opus throughout — `wa:dm`/`wa:group` acr values, `wa:group` scope, `wa_*`
   claims, `internal/oidp` package, consent-card preview, integration-guide tab, `wa.me`
   deep-link, STOP command.
4. **Secret hashing**: SHA-256 + pepper (gpt-5.5) over Argon2id — secrets are 256-bit random, KDF
   hardening adds hot-path cost without security benefit; matches better-auth's api-key posture.
5. **Defaults**: access/id_token TTL 900s (gpt-5.5, per-app configurable), auth code 60s (opus),
   pending request 600s (both), default mode `dm` when `acr_values` omitted and both enabled
   (opus; gpt-5.5's hard error rejected as hostile to simple integrations).
6. **State machines, attempt-cap numbers, observability & test plan**: gpt-5.5's, folded in.
7. **Per-app login command** (user addition, post-synthesis): `oauth_clients.login_command`
   (default `login`), snapshotted into the pending request, displayed on the consent page,
   asserted at claim time; the interceptor consumes the whole command namespace of a session's
   active apps so login traffic never reaches customer webhooks/events — even invalid attempts.
