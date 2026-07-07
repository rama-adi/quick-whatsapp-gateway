package oidp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
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

func NewPendingStore(rdb *redis.Client, prefix string, ttl time.Duration) *PendingStore {
	return &PendingStore{redis: rdb, prefix: strings.TrimSuffix(prefix, ":"), ttl: ttl}
}

func (s *PendingStore) Create(ctx context.Context, p PendingRequest) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if err := s.redis.Set(ctx, s.reqKey(p.ClientID, p.BrowserCode), raw, s.ttl).Err(); err != nil {
		return fmt.Errorf("oidp pending create req: %w", err)
	}
	if err := s.redis.Set(ctx, s.userCodeKey(p.SessionID, p.UserCode), p.ClientID+":"+p.BrowserCode, s.ttl).Err(); err != nil {
		_ = s.redis.Del(ctx, s.reqKey(p.ClientID, p.BrowserCode)).Err()
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
	keys, err := s.redis.Keys(ctx, s.reqKey("*", browserCode)).Result()
	if err != nil || len(keys) != 1 {
		return PendingRequest{}, redis.Nil
	}
	raw, err := s.redis.Get(ctx, keys[0]).Bytes()
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
	if err := s.redis.Set(ctx, s.reqKey(p.ClientID, p.BrowserCode), raw, time.Until(time.UnixMilli(p.ExpiresAt))).Err(); err != nil {
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
	if err := s.redis.Set(ctx, s.reqKey(p.ClientID, p.BrowserCode), raw, time.Minute).Err(); err != nil {
		return false, err
	}
	_ = s.redis.Del(ctx, s.userCodeKey(p.SessionID, p.UserCode)).Err()
	return true, s.Publish(ctx, browserCode, PendingStatusExpired)
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
func (s *PendingStore) ClaimVerified(ctx context.Context, in ClaimInput) (string, error) {
	verified, _ := json.Marshal(VerifiedBlock{
		LID: in.SenderLID, PhoneJID: in.PhoneJID, PhoneNumber: in.PhoneNumber,
		PushName: in.PushName, GroupJID: stringPtr(in.GroupJID), VerifiedAt: in.NowMs,
	})
	keys := []string{s.userCodeKey(in.SessionID, in.UserCode), s.key("oauth2:rl:verify:" + shaKey(in.SenderLID))}
	ref, _ := s.redis.Get(ctx, keys[0]).Result()
	res, err := claimScript.Run(ctx, s.redis, keys, in.SessionID, in.Mode, in.LoginCommand, string(verified), in.NowMs, int64(s.ttl/time.Millisecond), int64(300)).Text()
	if err == nil && res == PendingStatusVerified {
		parts := strings.SplitN(ref, ":", 2)
		if len(parts) == 2 {
			_ = s.Publish(ctx, parts[1], PendingStatusVerified)
		}
	}
	return res, err
}

var claimScript = redis.NewScript(`
local ref = redis.call("GET", KEYS[1])
if not ref then return "expired" end
local split = string.find(ref, ":")
if not split then return "expired" end
local client_id = string.sub(ref, 1, split - 1)
local browser_code = string.sub(ref, split + 1)
local req_key = string.match(KEYS[1], "^(.*oauth2:)usercode:") .. "req:" .. client_id .. ":" .. browser_code
local raw = redis.call("GET", req_key)
if not raw then redis.call("DEL", KEYS[1]); return "expired" end
local req = cjson.decode(raw)
if req.status ~= "pending" then return req.status end
if req.session_id ~= ARGV[1] or req.mode ~= ARGV[2] or req.login_command ~= ARGV[3] then return "wrong" end
req.status = "verified"
req.verified = cjson.decode(ARGV[4])
redis.call("SET", req_key, cjson.encode(req), "PX", ARGV[6])
redis.call("DEL", KEYS[1])
return "verified"
`)

func (s *PendingStore) reqKey(clientID, browserCode string) string {
	return s.key("oauth2:req:" + clientID + ":" + browserCode)
}
func (s *PendingStore) userCodeKey(sessionID, userCode string) string {
	return s.key("oauth2:usercode:" + sessionID + ":" + userCode)
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
