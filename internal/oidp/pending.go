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

type ClaimResult struct {
	Status      ClaimStatus
	BrowserCode string
	ClientID    string
	AppName     string
	Attempts    int
}

type PendingRequest struct {
	ClientID            string         `json:"client_id"`
	OrganizationID      string         `json:"organization_id"`
	SessionID           string         `json:"session_id"`
	RedirectURI         string         `json:"redirect_uri"`
	State               string         `json:"state,omitempty"`
	Nonce               string         `json:"nonce,omitempty"`
	CodeChallenge       string         `json:"code_challenge"`
	CodeChallengeMethod string         `json:"code_challenge_method"`
	Scopes              []string       `json:"scopes"`
	Mode                string         `json:"mode"`
	UserCode            string         `json:"user_code"`
	BrowserCode         string         `json:"browser_code"`
	LoginCommand        string         `json:"login_command"`
	AppName             string         `json:"app_name"`
	AppLogo             *string        `json:"app_logo,omitempty"`
	Target              PendingTarget  `json:"target"`
	Status              string         `json:"status"`
	Attempts            int            `json:"attempts"`
	Verified            *VerifiedBlock `json:"verified,omitempty"`
	CreatedAt           int64          `json:"created_at"`
	ExpiresAt           int64          `json:"expires_at"`
}

type PendingTarget struct {
	Mode      string  `json:"mode"`
	Number    *string `json:"number,omitempty"`
	BotName   *string `json:"bot_name,omitempty"`
	GroupName *string `json:"group_name,omitempty"`
}

type VerifiedBlock struct {
	LID         string  `json:"lid"`
	PhoneJID    string  `json:"phone_jid,omitempty"`
	PhoneNumber string  `json:"phone_number,omitempty"`
	PushName    string  `json:"push_name,omitempty"`
	GroupJID    *string `json:"group_jid,omitempty"`
	VerifiedAt  int64   `json:"verified_at"`
}

type PendingStore struct {
	redis  *redis.Client
	prefix string
	ttl    time.Duration
}

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

func NewPendingStore(rdb *redis.Client, prefix string, ttl time.Duration) *PendingStore {
	return &PendingStore{redis: rdb, prefix: strings.TrimSuffix(prefix, ":"), ttl: ttl}
}

func (s *PendingStore) Create(ctx context.Context, p PendingRequest) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if err := s.redis.Set(ctx, s.reqKey(p.BrowserCode), raw, s.ttl).Err(); err != nil {
		return fmt.Errorf("oidp pending create req: %w", err)
	}
	if err := s.redis.Set(ctx, s.userCodeKey(p.SessionID, p.UserCode), p.BrowserCode, s.ttl).Err(); err != nil {
		_ = s.redis.Del(ctx, s.reqKey(p.BrowserCode)).Err()
		return fmt.Errorf("oidp pending create reverse index: %w", err)
	}
	return nil
}

func (s *PendingStore) Load(ctx context.Context, browserCode string) (PendingRequest, error) {
	p, err := s.loadAny(ctx, browserCode)
	if err != nil {
		return PendingRequest{}, err
	}
	if p.ExpiresAt <= time.Now().UnixMilli() || p.Status == PendingStatusExpired {
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

func (s *PendingStore) Cancel(ctx context.Context, browserCode string) (bool, error) {
	p, err := s.Load(ctx, browserCode)
	if err != nil {
		return false, nil
	}
	if p.Status != PendingStatusPending {
		return true, nil
	}
	p.Status = PendingStatusDenied
	raw, _ := json.Marshal(p)
	if err := s.redis.Set(ctx, s.reqKey(p.BrowserCode), raw, time.Until(time.UnixMilli(p.ExpiresAt))).Err(); err != nil {
		return false, err
	}
	_ = s.redis.Del(ctx, s.userCodeKey(p.SessionID, p.UserCode)).Err()
	return true, s.Publish(ctx, browserCode, PendingStatusDenied)
}

func (s *PendingStore) Expire(ctx context.Context, browserCode string) (bool, error) {
	p, err := s.loadAny(ctx, browserCode)
	if err != nil {
		return false, nil
	}
	if p.Status != PendingStatusPending {
		return true, nil
	}
	p.Status = PendingStatusExpired
	raw, _ := json.Marshal(p)
	if err := s.redis.Set(ctx, s.reqKey(p.BrowserCode), raw, time.Minute).Err(); err != nil {
		return false, err
	}
	_ = s.redis.Del(ctx, s.userCodeKey(p.SessionID, p.UserCode)).Err()
	return true, s.Publish(ctx, browserCode, PendingStatusExpired)
}

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
			p.Status = PendingStatusExpired
			encoded, _ := json.Marshal(p)
			_ = s.redis.Set(ctx, key, encoded, time.Minute).Err()
			_ = s.redis.Del(ctx, s.userCodeKey(p.SessionID, p.UserCode)).Err()
			_ = s.Publish(ctx, p.BrowserCode, PendingStatusExpired)
		}
		if next == 0 {
			return nil
		}
		cursor = next
	}
}

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
			p.Status = PendingStatusDenied
			encoded, _ := json.Marshal(p)
			ttl := time.Until(time.UnixMilli(p.ExpiresAt))
			if ttl <= 0 {
				ttl = time.Minute
			}
			if err := s.redis.Set(ctx, key, encoded, ttl).Err(); err != nil {
				return denied, err
			}
			_ = s.redis.Del(ctx, s.userCodeKey(p.SessionID, p.UserCode)).Err()
			_ = s.Publish(ctx, p.BrowserCode, PendingStatusDenied)
			denied++
		}
		if next == 0 {
			return denied, nil
		}
		cursor = next
	}
}

func (s *PendingStore) Finalize(ctx context.Context, browserCode string) (PendingRequest, bool, error) {
	raw, err := finalizeScript.Run(ctx, s.redis, []string{s.key("oauth2:finalize:lock:" + shaKey(browserCode)), s.reqKey(browserCode)}, int64(time.Minute/time.Millisecond)).Text()
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
if req.status ~= "verified" then return cjson.encode({ok=false, request=req}) end
req.status = "finalized"
redis.call("SET", KEYS[2], cjson.encode(req), "PX", ARGV[1])
return cjson.encode({ok=true, request=req})
`)

func (s *PendingStore) StoreAuthCode(ctx context.Context, code string, payload AuthCode, ttl time.Duration) error {
	// Belt-and-braces: the Redis TTL is the primary expiry; the embedded stamp
	// guards redemption if the key outlives it (e.g. clock-frozen test stores).
	payload.ExpiresAtMS = time.Now().Add(ttl).UnixMilli()
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return s.redis.Set(ctx, s.authCodeKey(code), raw, ttl).Err()
}

var errAuthCodeExpired = errors.New("authorization code expired")

func (s *PendingStore) RedeemAuthCode(ctx context.Context, code string) (AuthCode, error) {
	raw, err := s.redis.GetDel(ctx, s.authCodeKey(code)).Bytes()
	if err != nil {
		return AuthCode{}, err
	}
	var payload AuthCode
	if err := json.Unmarshal(raw, &payload); err != nil {
		return AuthCode{}, err
	}
	if payload.ExpiresAtMS != 0 && time.Now().UnixMilli() > payload.ExpiresAtMS {
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

// ClaimVerified is the M6 gateway primitive. Lua argv signature:
// KEYS[1]=oauth2:usercode:<session_id>:<user_code>,
// KEYS[2]=oauth2:rl:verify:<sender_lid>;
// ARGV[1]=session_id, ARGV[2]=mode, ARGV[3]=login_command,
// ARGV[4]=verified_json, ARGV[5]=now_ms, ARGV[6]=request_ttl_ms,
// ARGV[7]=wrong_attempt_ttl_seconds.
func (s *PendingStore) ClaimVerified(ctx context.Context, in ClaimInput) (ClaimResult, error) {
	verified, _ := json.Marshal(VerifiedBlock{
		LID: in.SenderLID, PhoneJID: in.PhoneJID, PhoneNumber: in.PhoneNumber,
		PushName: in.PushName, GroupJID: stringPtr(in.GroupJID), VerifiedAt: in.NowMs,
	})
	keys := []string{s.userCodeKey(in.SessionID, in.UserCode), s.key("oauth2:rl:verify:" + shaKey(in.SenderLID))}
	raw, err := claimScript.Run(ctx, s.redis, keys, in.SessionID, in.Mode, strings.ToLower(in.LoginCommand), string(verified), in.NowMs, int64(s.ttl/time.Millisecond), int64(300)).Result()
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
local function bump(status)
  local sender_wrong = redis.call("INCR", KEYS[2])
  if sender_wrong == 1 then redis.call("EXPIRE", KEYS[2], ARGV[7]) end
  if sender_wrong > 5 then
    return cjson.encode({status="rate_limited", client_id=client_id, browser_code=browser_code, app_name=req.app_name or "", attempts=req.attempts or 0})
  end
  req.attempts = (req.attempts or 0) + 1
  if req.attempts >= 10 then
    req.status = "denied"
    redis.call("SET", req_key, cjson.encode(req), "PX", ARGV[6])
    redis.call("DEL", KEYS[1])
    redis.call("PUBLISH", "oauth2:login:" .. browser_code, "denied")
    return cjson.encode({status="denied", client_id=client_id, browser_code=browser_code, app_name=req.app_name or "", attempts=req.attempts})
  end
  redis.call("SET", req_key, cjson.encode(req), "PX", ARGV[6])
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
redis.call("SET", req_key, cjson.encode(req), "PX", ARGV[6])
redis.call("SET", KEYS[1], "__used__:" .. browser_code, "PX", 60000)
redis.call("PUBLISH", "oauth2:login:" .. browser_code, "verified")
return cjson.encode({status="verified", client_id=client_id, browser_code=browser_code, app_name=req.app_name or "", attempts=req.attempts or 0})
`)

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

func (s *PendingStore) RememberStop(ctx context.Context, senderLID, browserCode string, ttl time.Duration) error {
	return s.redis.Set(ctx, s.stopKey(senderLID), browserCode, ttl).Err()
}

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

func randomURLToken(bytes int) (string, error) {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
