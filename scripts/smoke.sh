#!/usr/bin/env bash
#
# smoke.sh — end-to-end trust-seam smoke test for the v2 split (masterplan §17 R5:
# "e2e smoke (login -> mint JWT -> start session -> pair -> send -> stream)").
#
# It drives the AUTOMATABLE slice of that path against an already-running stack
# (frontend better-auth + gateway, e.g. `docker compose up` from deploy/):
#
#   1. register a user           POST {BETTER_AUTH_URL}/api/auth/sign-up/email
#   2. mint a short-lived JWT    GET  {BETTER_AUTH_URL}/api/auth/token   (session cookie)
#   3. list sessions             GET  {GATEWAY_URL}/api/v1/sessions      -> assert 200
#   4. create a session          POST {GATEWAY_URL}/api/v1/sessions      -> assert 2xx
#   5. fetch its pairing QR      GET  {GATEWAY_URL}/api/v1/sessions/{id}/qr -> assert 200
#
# Steps 1-5 prove the seam end to end: a better-auth identity -> a JWKS-verified
# JWT -> an org-scoped, CORS-fronted gateway call that mutates WA-plane state.
#
# The remaining masterplan steps (pair -> send -> stream) need a REAL phone to
# scan the QR / link the device and are therefore MANUAL — printed as instructions
# at the end, not executed here.
#
# The script is strict: ANY unexpected HTTP status aborts with a non-zero exit and
# a dumped response body. Referenced from README.md ("Smoke test").
#
# Usage:
#   BETTER_AUTH_URL=http://localhost:3000 GATEWAY_URL=http://localhost:8080 \
#     scripts/smoke.sh
#
# Env (all optional, with the defaults below):
#   BETTER_AUTH_URL   frontend origin that serves /api/auth/*   (default http://localhost:3000)
#   GATEWAY_URL       gateway origin that serves /api/v1/*       (default http://localhost:8080)
#   SMOKE_EMAIL       account to register (default smoke+<epoch>@example.test)
#   SMOKE_PASSWORD    account password    (default smoke-Passw0rd!)
#   SMOKE_NAME        display name        (default Smoke Test)
#
# Requires: bash, curl, and one of jq (preferred) / python3 for JSON parsing.

set -euo pipefail

BETTER_AUTH_URL="${BETTER_AUTH_URL:-http://localhost:3000}"
GATEWAY_URL="${GATEWAY_URL:-http://localhost:8080}"
SMOKE_EMAIL="${SMOKE_EMAIL:-smoke+$(date +%s)@example.test}"
SMOKE_PASSWORD="${SMOKE_PASSWORD:-smoke-Passw0rd!}"
SMOKE_NAME="${SMOKE_NAME:-Smoke Test}"

# Strip any trailing slash so URL joins are clean.
BETTER_AUTH_URL="${BETTER_AUTH_URL%/}"
GATEWAY_URL="${GATEWAY_URL%/}"

COOKIE_JAR="$(mktemp -t smoke-cookies.XXXXXX)"
BODY_FILE="$(mktemp -t smoke-body.XXXXXX)"
trap 'rm -f "$COOKIE_JAR" "$BODY_FILE"' EXIT

# ---------------------------------------------------------------------------
# helpers
# ---------------------------------------------------------------------------

c_red=$'\033[31m'; c_grn=$'\033[32m'; c_ylw=$'\033[33m'; c_dim=$'\033[2m'; c_off=$'\033[0m'
# Disable color when not attached to a TTY.
[ -t 1 ] || { c_red=""; c_grn=""; c_ylw=""; c_dim=""; c_off=""; }

step() { printf '\n%s==>%s %s\n' "$c_ylw" "$c_off" "$1"; }
ok()   { printf '%s  ok%s %s\n' "$c_grn" "$c_off" "$1"; }
die()  { printf '%s FAIL%s %s\n' "$c_red" "$c_off" "$1" >&2; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || die "missing required tool: $1"; }
need curl

# json_get <file> <dot.path> — extract a string/number field. Prefers jq, falls
# back to python3. Exits non-zero (empty output) if the key is absent.
JSON_TOOL=""
if command -v jq >/dev/null 2>&1; then
  JSON_TOOL="jq"
elif command -v python3 >/dev/null 2>&1; then
  JSON_TOOL="python3"
else
  die "need either jq or python3 to parse JSON responses"
fi

json_get() {
  local file="$1" path="$2"
  if [ "$JSON_TOOL" = "jq" ]; then
    jq -er ".$path // empty" "$file" 2>/dev/null || true
  else
    python3 - "$file" "$path" <<'PY' 2>/dev/null || true
import json, sys
with open(sys.argv[1]) as f:
    data = json.load(f)
cur = data
for part in sys.argv[2].split('.'):
    if isinstance(cur, dict) and part in cur:
        cur = cur[part]
    else:
        sys.exit(0)
if cur is not None:
    print(cur)
PY
  fi
}

# request METHOD URL EXPECT_REGEX [curl-args...] — perform a request, capture the
# body to $BODY_FILE, and assert the status matches EXPECT_REGEX (e.g. '2..' or
# '200'). Always sends/stores the cookie jar so the better-auth session persists.
LAST_STATUS=""
request() {
  local method="$1" url="$2" expect="$3"; shift 3
  LAST_STATUS="$(curl -sS -o "$BODY_FILE" -w '%{http_code}' \
    -X "$method" \
    -b "$COOKIE_JAR" -c "$COOKIE_JAR" \
    "$@" \
    "$url")" || die "curl failed for $method $url (is the stack running?)"
  if ! [[ "$LAST_STATUS" =~ ^$expect$ ]]; then
    printf '%s  response body:%s\n' "$c_dim" "$c_off" >&2
    sed 's/^/    /' "$BODY_FILE" >&2 || true
    die "$method $url -> HTTP $LAST_STATUS, expected $expect"
  fi
}

# ---------------------------------------------------------------------------
# preflight
# ---------------------------------------------------------------------------

printf '%ssmoke config%s\n' "$c_dim" "$c_off"
printf '  BETTER_AUTH_URL = %s\n' "$BETTER_AUTH_URL"
printf '  GATEWAY_URL     = %s\n' "$GATEWAY_URL"
printf '  email           = %s\n' "$SMOKE_EMAIL"
printf '  json parser     = %s\n' "$JSON_TOOL"

step "gateway liveness (GET /healthz)"
request GET "$GATEWAY_URL/healthz" '2..'
ok "gateway is up (HTTP $LAST_STATUS)"

# ---------------------------------------------------------------------------
# 1. register a user (also opens a better-auth session via the cookie jar)
# ---------------------------------------------------------------------------

step "register user (POST /api/auth/sign-up/email)"
# better-auth's email/password sign-up returns 200 and sets a session cookie.
# USER_REGISTRATION_ENABLED must be true on the frontend for this to succeed.
request POST "$BETTER_AUTH_URL/api/auth/sign-up/email" '200' \
  -H 'Content-Type: application/json' \
  --data "$(printf '{"email":%s,"password":%s,"name":%s}' \
    "\"$SMOKE_EMAIL\"" "\"$SMOKE_PASSWORD\"" "\"$SMOKE_NAME\"")"
ok "registered $SMOKE_EMAIL (session cookie stored)"

# ---------------------------------------------------------------------------
# 2. mint a short-lived JWT from the session (GET /api/auth/token)
# ---------------------------------------------------------------------------

step "mint JWT (GET /api/auth/token)"
request GET "$BETTER_AUTH_URL/api/auth/token" '200'
JWT="$(json_get "$BODY_FILE" token)"
[ -n "$JWT" ] || { sed 's/^/    /' "$BODY_FILE" >&2; die "no 'token' field in /api/auth/token response"; }
# A JWT is three dot-separated segments; cheap shape check before we trust it.
case "$JWT" in
  *.*.*) ok "got JWT (${#JWT} chars; EdDSA, ~5 min expiry)";;
  *)     die "token does not look like a JWT: $JWT";;
esac
AUTH=(-H "Authorization: Bearer $JWT")

# ---------------------------------------------------------------------------
# 3. list sessions (GET /api/v1/sessions) — proves JWKS verify + org scoping
# ---------------------------------------------------------------------------

step "list sessions (GET /api/v1/sessions) — expect 200"
request GET "$GATEWAY_URL/api/v1/sessions" '200' "${AUTH[@]}"
ok "gateway accepted the JWT and returned the session list"

# ---------------------------------------------------------------------------
# 4. create a session (POST /api/v1/sessions) — expect 2xx (201 Created)
# ---------------------------------------------------------------------------

step "create session (POST /api/v1/sessions) — expect 2xx"
request POST "$GATEWAY_URL/api/v1/sessions" '2..' "${AUTH[@]}" \
  -H 'Content-Type: application/json' \
  --data '{"label":"smoke-test","start":true}'
SESSION_ID="$(json_get "$BODY_FILE" id)"
[ -n "$SESSION_ID" ] || { sed 's/^/    /' "$BODY_FILE" >&2; die "created session but no 'id' in response"; }
ok "created session $SESSION_ID (HTTP $LAST_STATUS)"

# ---------------------------------------------------------------------------
# 5. fetch the pairing QR (GET /api/v1/sessions/{id}/qr) — expect 200
# ---------------------------------------------------------------------------

step "fetch pairing QR (GET /api/v1/sessions/$SESSION_ID/qr) — expect 200"
request GET "$GATEWAY_URL/api/v1/sessions/$SESSION_ID/qr" '200' "${AUTH[@]}"
QR_CODE="$(json_get "$BODY_FILE" code)"
[ -n "$QR_CODE" ] || { sed 's/^/    /' "$BODY_FILE" >&2; die "QR response had no 'code' field"; }
ok "got a pairing QR code for session $SESSION_ID"

# ---------------------------------------------------------------------------
# done — automatable path is green
# ---------------------------------------------------------------------------

printf '\n%sAUTOMATED SMOKE PASSED%s — register -> JWT -> list -> create -> QR all green.\n' \
  "$c_grn" "$c_off"

cat <<EOF

${c_ylw}MANUAL STEPS (require a real phone — not automatable here)${c_off}
-----------------------------------------------------------------
The masterplan path continues "pair -> send -> stream". Finish it by hand:

  a. PAIR: render the QR string above as a QR image and scan it from
     WhatsApp on a phone (Settings -> Linked Devices -> Link a Device).
     The QR rotates; refetch with:
       curl -H "Authorization: Bearer \$JWT" \\
         "$GATEWAY_URL/api/v1/sessions/$SESSION_ID/qr?format=image"
     Alternatively request a pairing code instead of scanning:
       curl -X POST -H "Authorization: Bearer \$JWT" \\
         -H 'Content-Type: application/json' --data '{"phone":"<E.164>"}' \\
         "$GATEWAY_URL/api/v1/sessions/$SESSION_ID/pairing-code"
     Wait until GET .../sessions/$SESSION_ID/me reports the session paired.

  b. SEND: send yourself a message (JWTs are ~5 min — mint a fresh one):
       curl -X POST -H "Authorization: Bearer \$JWT" \\
         -H 'Content-Type: application/json' \\
         --data '{"type":"text","to":"<E.164>@s.whatsapp.net","text":"hello from smoke"}' \\
         "$GATEWAY_URL/api/v1/sessions/$SESSION_ID/messages"

  c. STREAM: watch live events (NDJSON) while the send lands:
       curl -N -H "Authorization: Bearer \$JWT" "$GATEWAY_URL/api/v1/events"

Cleanup when finished:
       curl -X DELETE -H "Authorization: Bearer \$JWT" \\
         "$GATEWAY_URL/api/v1/sessions/$SESSION_ID"
EOF
