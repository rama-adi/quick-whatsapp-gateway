package oidp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	PendingStatusPending   = "pending"
	PendingStatusVerified  = "verified"
	PendingStatusFinalized = "finalized"
	PendingStatusDenied    = "denied"
	PendingStatusExpired   = "expired"
)

// ClaimStatus is the stable outcome of atomically attempting a WhatsApp login
// code. Mismatch outcomes are deliberately distinct for feedback and auditing.
type ClaimStatus string

const (
	ClaimStatusVerified        ClaimStatus = "verified"
	ClaimStatusDenied          ClaimStatus = "denied"
	ClaimStatusExpired         ClaimStatus = "expired"
	ClaimStatusWrong           ClaimStatus = "wrong"
	ClaimStatusRateLimited     ClaimStatus = "rate_limited"
	ClaimStatusAlreadyUsed     ClaimStatus = "already_used"
	ClaimStatusModeMismatch    ClaimStatus = "mode_mismatch"
	ClaimStatusCommandMismatch ClaimStatus = "command_mismatch"
)

// ClaimResult identifies the pending browser flow affected by a code claim.
type ClaimResult struct {
	Status      ClaimStatus
	BrowserCode string
	ClientID    string
	AppName     string
	Attempts    int
}

// PendingRequest is the Redis-backed state machine joining a browser OAuth
// authorization request to its WhatsApp verification message.
type PendingRequest struct {
	ClientID            string          `json:"client_id"`
	OrganizationID      string          `json:"organization_id"`
	SessionID           string          `json:"session_id"`
	RedirectURI         string          `json:"redirect_uri"`
	State               string          `json:"state,omitempty"`
	Nonce               string          `json:"nonce,omitempty"`
	CodeChallenge       string          `json:"code_challenge"`
	CodeChallengeMethod string          `json:"code_challenge_method"`
	Scopes              []string        `json:"scopes"`
	Mode                string          `json:"mode"`
	UserCode            string          `json:"user_code"`
	BrowserCode         string          `json:"browser_code"`
	LoginCommand        string          `json:"login_command"`
	AppName             string          `json:"app_name"`
	AppLogo             *string         `json:"app_logo,omitempty"`
	Target              PendingTarget   `json:"target"`
	Status              string          `json:"status"`
	Attempts            int             `json:"attempts"`
	Verified            *VerifiedBlock  `json:"verified,omitempty"`
	Finalized           *FinalizedBlock `json:"finalized,omitempty"`
	CreatedAt           int64           `json:"created_at"`
	ExpiresAt           int64           `json:"expires_at"`
}

// PendingTarget describes the DM or group in which verification must occur.
type PendingTarget struct {
	Mode      string  `json:"mode"`
	Number    *string `json:"number,omitempty"`
	BotName   *string `json:"bot_name,omitempty"`
	GroupName *string `json:"group_name,omitempty"`
}

// VerifiedBlock is the immutable WhatsApp identity captured by verification.
type VerifiedBlock struct {
	LID         string  `json:"lid"`
	PhoneJID    string  `json:"phone_jid,omitempty"`
	PhoneNumber string  `json:"phone_number,omitempty"`
	PushName    string  `json:"push_name,omitempty"`
	GroupJID    *string `json:"group_jid,omitempty"`
	VerifiedAt  int64   `json:"verified_at"`
}

// FinalizedBlock records the one-time authorization code and redirect selected
// when a verified request is finalized.
type FinalizedBlock struct {
	Code     string `json:"code"`
	Redirect string `json:"redirect"`
}

// PendingStore owns short-lived OAuth request, reverse user-code, rate-limit,
// and authorization-code keys. State changes that race verification,
// cancellation, expiry, or finalization are implemented as Redis scripts so a
// stale reader cannot overwrite a newer terminal state.
type PendingStore struct {
	redis  *redis.Client
	prefix string
	ttl    time.Duration
	now    func() time.Time
}

// AuthCode is the single-use payload stored under a hashed authorization code.
// RedeemAuthCode removes it atomically before validating its embedded expiry.
type AuthCode struct {
	GrantID             string   `json:"grant_id"`
	ClientID            string   `json:"client_id"`
	RedirectURI         string   `json:"redirect_uri"`
	Scopes              []string `json:"scopes"`
	Nonce               string   `json:"nonce,omitempty"`
	CodeChallenge       string   `json:"code_challenge"`
	CodeChallengeMethod string   `json:"code_challenge_method"`
	ACR                 string   `json:"acr"`
	AuthTime            int64    `json:"auth_time"`
	ExpiresAtMS         int64    `json:"expires_at_ms,omitempty"`
}

// NewPendingStore constructs the Redis-backed authorization handshake store.
// prefix namespaces every request, reverse index, counter, and one-time code;
// ttl is the default browser-request lifetime, while individual terminal and
// authorization-code transitions may choose shorter retention windows.
func NewPendingStore(rdb *redis.Client, prefix string, ttl time.Duration) *PendingStore {
	return &PendingStore{redis: rdb, prefix: strings.TrimSuffix(prefix, ":"), ttl: ttl, now: time.Now}
}

// SetClock overrides the store's time source. Production uses the default wall
// clock; tests inject a fixed clock so ExpiresAt stamps and expiry checks agree.
func (s *PendingStore) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}

func (s *PendingStore) clock() time.Time {
	if s.now == nil {
		return time.Now()
	}
	return s.now()
}

// until is the remaining lifetime until an epoch-ms deadline, measured against
// the store's clock (so tests with an injected clock agree with the ExpiresAt
// stamps written under that same clock).
func (s *PendingStore) until(expiresAtMS int64) time.Duration {
	return time.UnixMilli(expiresAtMS).Sub(s.clock())
}

// Create atomically reserves both BrowserCode and the session-scoped UserCode.
// The supplied ExpiresAt is the authoritative absolute deadline for both keys;
// an already-expired request or collision leaves neither key changed.
func (s *PendingStore) Create(ctx context.Context, p PendingRequest) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	ttl := s.until(p.ExpiresAt)
	if ttl <= 0 {
		return fmt.Errorf("oidp pending create expired request")
	}
	ok, err := createPendingScript.Run(ctx, s.redis, []string{s.reqKey(p.BrowserCode), s.userCodeKey(p.SessionID, p.UserCode)}, string(raw), p.BrowserCode, p.ExpiresAt).Bool()
	if err != nil {
		return fmt.Errorf("oidp pending create: %w", err)
	}
	if !ok {
		return fmt.Errorf("oidp pending create reverse index: user code collision")
	}
	return nil
}

var createPendingScript = redis.NewScript(`
if redis.call("EXISTS", KEYS[1]) > 0 then return false end
local ref = redis.call("GET", KEYS[2])
if ref and ref ~= "" then return false end
redis.call("SET", KEYS[1], ARGV[1], "PXAT", ARGV[3])
redis.call("SET", KEYS[2], ARGV[2], "PXAT", ARGV[3])
return true
`)

// Load returns a browser-visible request if it still exists and its embedded
// deadline has not passed. Logical expiry is enforced even if Redis has not yet
// evicted the key, preventing TTL scheduling delay from extending authorization.
func (s *PendingStore) Load(ctx context.Context, browserCode string) (PendingRequest, error) {
	p, err := s.loadAny(ctx, browserCode)
	if err != nil {
		return PendingRequest{}, err
	}
	if p.ExpiresAt <= s.clock().UnixMilli() || p.Status == PendingStatusExpired {
		return PendingRequest{}, redis.Nil
	}
	return p, nil
}

func (s *PendingStore) loadAny(ctx context.Context, browserCode string) (PendingRequest, error) {
	raw, err := s.redis.Get(ctx, s.reqKey(browserCode)).Bytes()
	if err != nil {
		return PendingRequest{}, err
	}
	var p PendingRequest
	if err := json.Unmarshal(raw, &p); err != nil {
		return PendingRequest{}, err
	}
	return p, nil
}

// Cancel atomically moves a pending request to denied. It cannot overwrite a
// verified or finalized winner; the boolean reports that the request key exists,
// while the script separately suppresses duplicate state publication.
func (s *PendingStore) Cancel(ctx context.Context, browserCode string) (bool, error) {
	exists, _, err := s.transition(ctx, browserCode, PendingStatusDenied, "", "", false, 0)
	return exists, err
}

// Expire atomically marks a still-pending request expired and publishes the new
// state for wait streams. Verified and finalized requests remain untouched.
func (s *PendingStore) Expire(ctx context.Context, browserCode string) (bool, error) {
	exists, _, err := s.transition(ctx, browserCode, PendingStatusExpired, "", "", false, time.Minute)
	return exists, err
}

// transition atomically checks and changes a pending request, removes its
// reverse user-code index, and publishes the terminal state. The compare-and-set
// prevents cancellation/expiry scans from overwriting a concurrent verification
// or finalization.
func (s *PendingStore) transition(ctx context.Context, browserCode, status, sessionID, clientID string, allowVerified bool, retain time.Duration) (exists, changed bool, err error) {
	state, err := transitionScript.Run(ctx, s.redis, []string{s.reqKey(browserCode)},
		status, sessionID, clientID, boolInt(allowVerified), retain.Milliseconds(), s.clock().UnixMilli()).Int64()
	if err != nil {
		return false, false, err
	}
	return state != 0, state == 2, nil
}

var transitionScript = redis.NewScript(`
local raw = redis.call("GET", KEYS[1])
if not raw then return 0 end
local req = cjson.decode(raw)
if ARGV[2] ~= "" and (req.session_id or "") ~= ARGV[2] then return 0 end
if ARGV[3] ~= "" and (req.client_id or "") ~= ARGV[3] then return 0 end
local allowed = req.status == "pending" or (ARGV[4] == "1" and req.status == "verified")
if not allowed then return 1 end
req.status = ARGV[1]
local encoded = cjson.encode(req)
local retain_ms = tonumber(ARGV[5])
local expires_at = tonumber(req.expires_at or 0)
if retain_ms > 0 then
  redis.call("SET", KEYS[1], encoded, "PX", retain_ms)
elseif expires_at > tonumber(ARGV[6]) then
  redis.call("SET", KEYS[1], encoded, "PXAT", expires_at)
else
  redis.call("SET", KEYS[1], encoded, "PX", 60000)
end
local prefix = string.match(KEYS[1], "^(.*oauth2:)req:") or "oauth2:"
redis.call("DEL", prefix .. "usercode:" .. (req.session_id or "") .. ":" .. (req.user_code or ""))
redis.call("PUBLISH", "oauth2:login:" .. (req.browser_code or ""), ARGV[1])
return 2
`)

// ExpireBySession invalidates every open request reverse-indexed to one WhatsApp
// session and publishes expiry for active browser streams. Each request uses the
// same guarded transition as Expire, so concurrent verification remains the
// winner instead of being overwritten by session invalidation.
func (s *PendingStore) ExpireBySession(ctx context.Context, sessionID string) error {
	var cursor uint64
	pattern := s.key("oauth2:req:*")
	for {
		keys, next, err := s.redis.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return err
		}
		for _, key := range keys {
			raw, err := s.redis.Get(ctx, key).Bytes()
			if err != nil {
				continue
			}
			var p PendingRequest
			if err := json.Unmarshal(raw, &p); err != nil || p.SessionID != sessionID || p.Status != PendingStatusPending {
				continue
			}
			if _, _, err := s.transition(ctx, p.BrowserCode, PendingStatusExpired, sessionID, "", false, time.Minute); err != nil {
				return err
			}
		}
		if next == 0 {
			return nil
		}
		cursor = next
	}
}

// DenyClientPending denies every pending or verified request for a changed or
// disabled OAuth client. It returns the number actually transitioned and
// publishes each denial; finalized requests remain immutable.
func (s *PendingStore) DenyClientPending(ctx context.Context, clientID string) (int, error) {
	var cursor uint64
	var denied int
	pattern := s.key("oauth2:req:*")
	for {
		keys, next, err := s.redis.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return denied, err
		}
		for _, key := range keys {
			raw, err := s.redis.Get(ctx, key).Bytes()
			if err != nil {
				continue
			}
			var p PendingRequest
			if err := json.Unmarshal(raw, &p); err != nil || p.ClientID != clientID {
				continue
			}
			if p.Status != PendingStatusPending && p.Status != PendingStatusVerified {
				continue
			}
			_, changed, err := s.transition(ctx, p.BrowserCode, PendingStatusDenied, "", clientID, true, 0)
			if err != nil {
				return denied, err
			}
			if changed {
				denied++
			}
		}
		if next == 0 {
			return denied, nil
		}
		cursor = next
	}
}

// Finalize atomically changes a verified request to finalized and stores its
// single-use authorization code under a hashed key. A repeated finalization
// returns the existing finalized request as successful, while a code collision
// changes neither record, keeping redirect and token redemption idempotent.
func (s *PendingStore) Finalize(ctx context.Context, browserCode string, finalized FinalizedBlock, authCode AuthCode, authTTL time.Duration) (PendingRequest, bool, error) {
	authCode.ExpiresAtMS = s.clock().Add(authTTL).UnixMilli()
	payload, err := json.Marshal(authCode)
	if err != nil {
		return PendingRequest{}, false, err
	}
	raw, err := finalizeScript.Run(ctx, s.redis, []string{s.key("oauth2:finalize:lock:" + shaKey(browserCode)), s.reqKey(browserCode), s.authCodeKey(finalized.Code)}, s.clock().UnixMilli(), finalized.Code, finalized.Redirect, string(payload), authCode.ExpiresAtMS).Text()
	if err != nil {
		return PendingRequest{}, false, err
	}
	var out struct {
		OK      bool           `json:"ok"`
		Request PendingRequest `json:"request"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return PendingRequest{}, false, err
	}
	return out.Request, out.OK, nil
}

var finalizeScript = redis.NewScript(`
local raw = redis.call("GET", KEYS[2])
if not raw then return cjson.encode({ok=false}) end
local req = cjson.decode(raw)
if (req.expires_at or 0) <= tonumber(ARGV[1]) then return cjson.encode({ok=false, request=req}) end
if req.status == "finalized" then return cjson.encode({ok=true, request=req}) end
if req.status ~= "verified" then return cjson.encode({ok=false, request=req}) end
req.status = "finalized"
req.finalized = {code=ARGV[2], redirect=ARGV[3]}
redis.call("SET", KEYS[3], ARGV[4], "PXAT", ARGV[5])
redis.call("SET", KEYS[2], cjson.encode(req), "PXAT", req.expires_at)
return cjson.encode({ok=true, request=req})
`)

func (s *PendingStore) StoreAuthCode(ctx context.Context, code string, payload AuthCode, ttl time.Duration) error {
	// Belt-and-braces: the Redis TTL is the primary expiry; the embedded stamp
	// guards redemption if the key outlives it (e.g. clock-frozen test stores).
	payload.ExpiresAtMS = s.clock().Add(ttl).UnixMilli()
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return s.redis.Set(ctx, s.authCodeKey(code), raw, ttl).Err()
}

var errAuthCodeExpired = errors.New("authorization code expired")

// RedeemAuthCode consumes an authorization code with Redis GETDEL before decoding
// and checking its embedded expiry. Consumption is intentionally fail-closed:
// malformed or expired payloads cannot be retried or raced by another redeemer.
func (s *PendingStore) RedeemAuthCode(ctx context.Context, code string) (AuthCode, error) {
	raw, err := s.redis.GetDel(ctx, s.authCodeKey(code)).Bytes()
	if err != nil {
		return AuthCode{}, err
	}
	var payload AuthCode
	if err := json.Unmarshal(raw, &payload); err != nil {
		return AuthCode{}, err
	}
	if payload.ExpiresAtMS != 0 && s.clock().UnixMilli() > payload.ExpiresAtMS {
		return AuthCode{}, errAuthCodeExpired
	}
	return payload, nil
}

func (s *PendingStore) Publish(ctx context.Context, browserCode, status string) error {
	return s.redis.Publish(ctx, loginChannel(browserCode), status).Err()
}

func (s *PendingStore) Subscribe(ctx context.Context, browserCode string) *redis.PubSub {
	return s.redis.Subscribe(ctx, loginChannel(browserCode))
}

func (s *PendingStore) IncrementMint(ctx context.Context, sessionID string, limit int, window time.Duration) (bool, error) {
	key := s.key("oauth2:rl:mint:" + sessionID)
	n, err := s.redis.Incr(ctx, key).Result()
	if err != nil {
		return false, err
	}
	if n == 1 {
		_ = s.redis.Expire(ctx, key, window).Err()
	}
	return n <= int64(limit), nil
}

func (s *PendingStore) ReserveUserCode(ctx context.Context, sessionID, userCode string, expiresAt int64) (bool, error) {
	ttl := s.until(expiresAt)
	if ttl <= 0 {
		return false, nil
	}
	return s.redis.SetNX(ctx, s.userCodeKey(sessionID, userCode), "", ttl).Result()
}

// ClaimInput carries the normalized WhatsApp message identity and request
// attributes checked by ClaimVerified's atomic state transition.
// ClaimInput is the complete comparison set used by the atomic WhatsApp claim
// script. Browser/session/client/mode/command bind the message to one request;
// claimant fields become immutable VerifiedBlock data only on a winning claim.
type ClaimInput struct {
	SessionID    string
	UserCode     string
	Mode         string
	LoginCommand string
	SenderLID    string
	PhoneJID     string
	PhoneNumber  string
	PushName     string
	GroupJID     string
	NowMs        int64
}

// verifiedGrace caps how long a verified-but-unfinalized flow stays
// finalizable. A live consent page finalizes within seconds; the grace only
// has to cover a backgrounded mobile tab being reloaded on return. Keeping it
// far below the request TTL bounds the window in which an abandoned verified
// flow on a shared computer could still be auto-finalized (oauth.md §7.8).
const verifiedGrace = 3 * time.Minute

// ClaimVerified is the M6 gateway primitive. Lua argv signature:
// KEYS[1]=oauth2:usercode:<session_id>:<user_code>,
// KEYS[2]=oauth2:rl:verify:<sender_lid>;
// ARGV[1]=session_id, ARGV[2]=mode, ARGV[3]=login_command,
// ARGV[4]=verified_json, ARGV[5]=now_ms, ARGV[6]=request_ttl_ms,
// ARGV[7]=wrong_attempt_ttl_seconds, ARGV[8]=verified_grace_ms.
// ClaimVerified atomically resolves the session-scoped user-code index, validates
// request bindings and expiry, applies the attempt cap, and selects at most one
// verified winner. Duplicate delivery observes already_used, while mismatches
// consume attempts without extending either request TTL. The winning claim
// shortens the request deadline to min(expires_at, now + verified_grace_ms).
func (s *PendingStore) ClaimVerified(ctx context.Context, in ClaimInput) (ClaimResult, error) {
	verified, _ := json.Marshal(VerifiedBlock{
		LID: in.SenderLID, PhoneJID: in.PhoneJID, PhoneNumber: in.PhoneNumber,
		PushName: in.PushName, GroupJID: stringPtr(in.GroupJID), VerifiedAt: in.NowMs,
	})
	keys := []string{s.userCodeKey(in.SessionID, in.UserCode), s.key("oauth2:rl:verify:" + shaKey(in.SenderLID))}
	raw, err := claimScript.Run(ctx, s.redis, keys, in.SessionID, in.Mode, strings.ToLower(in.LoginCommand), string(verified), in.NowMs, int64(s.ttl/time.Millisecond), int64(300), verifiedGrace.Milliseconds()).Result()
	if err != nil {
		return ClaimResult{}, err
	}
	return decodeClaim(raw)
}

var claimScript = redis.NewScript(`
local ref = redis.call("GET", KEYS[1])
if not ref then return cjson.encode({status="expired"}) end
if string.sub(ref, 1, 9) == "__used__:" then
  return cjson.encode({status="already_used", browser_code=string.sub(ref, 10)})
end
local browser_code = ref
local req_key = string.match(KEYS[1], "^(.*oauth2:)usercode:") .. "req:" .. browser_code
local raw = redis.call("GET", req_key)
if not raw then redis.call("DEL", KEYS[1]); return cjson.encode({status="expired", browser_code=browser_code}) end
local req = cjson.decode(raw)
local client_id = req.client_id or ""
local now_ms = tonumber(ARGV[5])
local expires_at = tonumber(req.expires_at or 0)
if expires_at <= now_ms then
  redis.call("DEL", KEYS[1])
  return cjson.encode({status="expired", client_id=client_id, browser_code=browser_code, app_name=req.app_name or "", attempts=req.attempts or 0})
end
local function bump(status)
  local sender_wrong = redis.call("INCR", KEYS[2])
  if sender_wrong == 1 then redis.call("EXPIRE", KEYS[2], ARGV[7]) end
  if sender_wrong > 5 then
    return cjson.encode({status="rate_limited", client_id=client_id, browser_code=browser_code, app_name=req.app_name or "", attempts=req.attempts or 0})
  end
  req.attempts = (req.attempts or 0) + 1
  if req.attempts >= 10 then
    req.status = "denied"
    redis.call("SET", req_key, cjson.encode(req), "PXAT", expires_at)
    redis.call("DEL", KEYS[1])
    redis.call("PUBLISH", "oauth2:login:" .. browser_code, "denied")
    return cjson.encode({status="denied", client_id=client_id, browser_code=browser_code, app_name=req.app_name or "", attempts=req.attempts})
  end
  redis.call("SET", req_key, cjson.encode(req), "PXAT", expires_at)
  return cjson.encode({status=status, client_id=client_id, browser_code=browser_code, app_name=req.app_name or "", attempts=req.attempts})
end
if req.status == "verified" or req.status == "finalized" then
  return cjson.encode({status="already_used", client_id=client_id, browser_code=browser_code, app_name=req.app_name or "", attempts=req.attempts or 0})
end
if req.status ~= "pending" then
  return cjson.encode({status=req.status, client_id=client_id, browser_code=browser_code, app_name=req.app_name or "", attempts=req.attempts or 0})
end
if req.session_id ~= ARGV[1] or req.mode ~= ARGV[2] then
  return bump("mode_mismatch")
end
if string.lower(req.login_command or "") ~= ARGV[3] then
  return bump("command_mismatch")
end
req.status = "verified"
req.verified = cjson.decode(ARGV[4])
-- Verified flows must finalize promptly: shorten the deadline to the grace
-- window so an abandoned verified flow cannot linger for the full request TTL.
local verified_until = expires_at
local grace_until = now_ms + tonumber(ARGV[8])
if grace_until < verified_until then verified_until = grace_until end
req.expires_at = verified_until
redis.call("SET", req_key, cjson.encode(req), "PXAT", verified_until)
local used_until = verified_until
local used_min = now_ms + 60000
if used_min < used_until then used_until = used_min end
redis.call("SET", KEYS[1], "__used__:" .. browser_code, "PXAT", used_until)
redis.call("PUBLISH", "oauth2:login:" .. browser_code, "verified")
return cjson.encode({status="verified", client_id=client_id, browser_code=browser_code, app_name=req.app_name or "", attempts=req.attempts or 0})
`)

// ClaimWrongCode increments the per-session, per-sender abuse counter when no
// reverse user-code match exists. Redis performs the increment and window setup
// atomically; reaching the cap returns rate_limited without identifying requests.
func (s *PendingStore) ClaimWrongCode(ctx context.Context, sessionID, senderLID string, nowMs int64) (ClaimResult, error) {
	raw, err := wrongCodeScript.Run(ctx, s.redis, []string{s.key("oauth2:rl:verify:" + shaKey(senderLID))}, sessionID, nowMs, int64(300)).Result()
	if err != nil {
		return ClaimResult{}, err
	}
	return decodeClaim(raw)
}

var wrongCodeScript = redis.NewScript(`
local n = redis.call("INCR", KEYS[1])
if n == 1 then redis.call("EXPIRE", KEYS[1], ARGV[3]) end
if n > 5 then return cjson.encode({status="rate_limited", attempts=n}) end
return cjson.encode({status="wrong", attempts=n})
`)

// RememberStop records the sender's most recent verified browser flow for the
// bounded interval in which a WhatsApp STOP command may revoke it.
func (s *PendingStore) RememberStop(ctx context.Context, senderLID, browserCode string, ttl time.Duration) error {
	return s.redis.Set(ctx, s.stopKey(senderLID), browserCode, ttl).Err()
}

// DenyRecentForSender consumes the sender's STOP pointer and atomically denies
// the referenced verified request. Replays cannot repeatedly mutate the flow,
// and a request finalized first remains final rather than being revoked here.
func (s *PendingStore) DenyRecentForSender(ctx context.Context, senderLID string) (ClaimResult, error) {
	key := s.stopKey(senderLID)
	browserCode, err := s.redis.Get(ctx, key).Result()
	if err != nil {
		return ClaimResult{Status: ClaimStatusExpired}, nil
	}
	raw, err := stopScript.Run(ctx, s.redis, []string{s.key("oauth2:stop:block:" + shaKey(senderLID+":"+browserCode)), key}, browserCode, int64(300000)).Result()
	if err != nil {
		return ClaimResult{}, err
	}
	return decodeClaim(raw)
}

var stopScript = redis.NewScript(`
local browser_code = ARGV[1]
local req_key = string.match(KEYS[2], "^(.*oauth2:)stop:sender:") .. "req:" .. browser_code
local raw = redis.call("GET", req_key)
if not raw then return cjson.encode({status="expired", browser_code=browser_code}) end
local req = cjson.decode(raw)
if req.status ~= "verified" then
  return cjson.encode({status=req.status, client_id=req.client_id or "", browser_code=browser_code, app_name=req.app_name or "", attempts=req.attempts or 0})
end
req.status = "denied"
redis.call("SET", req_key, cjson.encode(req), "PX", ARGV[2])
redis.call("DEL", string.match(req_key, "^(.*oauth2:)req:") .. "usercode:" .. req.session_id .. ":" .. req.user_code)
redis.call("DEL", KEYS[2])
redis.call("SET", KEYS[1], "1", "PX", ARGV[2])
redis.call("PUBLISH", "oauth2:login:" .. browser_code, "denied")
return cjson.encode({status="denied", client_id=req.client_id or "", browser_code=browser_code, app_name=req.app_name or "", attempts=req.attempts or 0})
`)

func decodeClaim(raw any) (ClaimResult, error) {
	var b []byte
	switch v := raw.(type) {
	case string:
		b = []byte(v)
	case []byte:
		b = v
	default:
		b, _ = json.Marshal(v)
	}
	var out struct {
		Status      ClaimStatus `json:"status"`
		BrowserCode string      `json:"browser_code"`
		ClientID    string      `json:"client_id"`
		AppName     string      `json:"app_name"`
		Attempts    int         `json:"attempts"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return ClaimResult{}, err
	}
	return ClaimResult(out), nil
}

func (s *PendingStore) reqKey(browserCode string) string {
	return s.key("oauth2:req:" + browserCode)
}
func (s *PendingStore) userCodeKey(sessionID, userCode string) string {
	return s.key("oauth2:usercode:" + sessionID + ":" + userCode)
}
func (s *PendingStore) authCodeKey(code string) string {
	return s.key("oauth2:authcode:" + shaKey(code))
}
func (s *PendingStore) stopKey(senderLID string) string {
	return s.key("oauth2:stop:sender:" + shaKey(senderLID))
}
func (s *PendingStore) key(k string) string {
	if s.prefix == "" {
		return k
	}
	return s.prefix + ":" + k
}
func loginChannel(browserCode string) string { return "oauth2:login:" + browserCode }
func shaKey(v string) string {
	sum := sha256.Sum256([]byte(v))
	return fmt.Sprintf("%x", sum[:])
}
func stringPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func randomURLToken(bytes int) (string, error) {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
