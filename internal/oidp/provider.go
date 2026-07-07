package oidp

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

const (
	DefaultRequestTTL  = 10 * time.Minute
	DefaultAuthCodeTTL = time.Minute
	defaultMintLimit   = 30
	streamHeartbeat    = 20 * time.Second
)

type OAuthClientReader interface {
	GetActiveByClientID(ctx context.Context, clientID string) (domain.OAuthClient, error)
}

type SessionReader interface {
	Get(ctx context.Context, id string) (domain.WASession, error)
}

type GroupReader interface {
	GetByJID(ctx context.Context, groupJID string) (domain.Group, error)
}

type ProviderConfig struct {
	Clients     OAuthClientReader
	Sessions    SessionReader
	Groups      GroupReader
	Pending     *PendingStore
	WebLoginURL string
	RequestTTL  time.Duration
	Now         func() time.Time
}

type Provider struct {
	clients     OAuthClientReader
	sessions    SessionReader
	groups      GroupReader
	pending     *PendingStore
	webLoginURL string
	requestTTL  time.Duration
	now         func() time.Time
	streamMu    sync.Mutex
	byCode      map[string]int
	byIP        map[string]int
}

func NewProvider(cfg ProviderConfig) *Provider {
	ttl := cfg.RequestTTL
	if ttl <= 0 {
		ttl = DefaultRequestTTL
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Provider{clients: cfg.Clients, sessions: cfg.Sessions, groups: cfg.Groups, pending: cfg.Pending, webLoginURL: strings.TrimRight(cfg.WebLoginURL, "/"), requestTTL: ttl, now: now, byCode: map[string]int{}, byIP: map[string]int{}}
}

func (p *Provider) Mount(r chi.Router) {
	r.Get("/oauth/authorize", p.HandleAuthorize)
	r.Get("/oauth/wait/{browser_code}/stream", p.HandleWaitStream)
	r.Post("/oauth/wait/{browser_code}/cancel", p.HandleCancel)
}

func (p *Provider) HandleAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	clientID, redirectURI := q.Get("client_id"), q.Get("redirect_uri")
	client, err := p.clients.GetActiveByClientID(r.Context(), clientID)
	if clientID == "" || err != nil {
		localOAuthError(w, http.StatusBadRequest, "invalid_client", "Unknown or disabled client.")
		return
	}
	if !containsJSON(client.RedirectURIs, redirectURI) {
		localOAuthError(w, http.StatusBadRequest, "invalid_request", "Invalid redirect_uri.")
		return
	}
	if err := validateAuthorizeBasics(q); err != nil {
		redirectOAuthError(w, redirectURI, err.Error(), q.Get("state"))
		return
	}
	scopes, err := resolveScopes(client.AllowedScopes, q.Get("scope"))
	if err != nil {
		redirectOAuthError(w, redirectURI, "invalid_scope", q.Get("state"))
		return
	}
	mode, err := resolveMode(client.Modes, q.Get("acr_values"))
	if err != nil {
		redirectOAuthError(w, redirectURI, "invalid_request", q.Get("state"))
		return
	}
	sess, err := p.sessions.Get(r.Context(), client.SessionID)
	if err != nil || sess.OrganizationID != client.OrganizationID || sess.Status != domain.SessionWorking {
		redirectOAuthError(w, redirectURI, "temporarily_unavailable", q.Get("state"))
		return
	}
	allowed, err := p.pending.IncrementMint(r.Context(), client.SessionID, defaultMintLimit, p.requestTTL)
	if err != nil || !allowed {
		redirectOAuthError(w, redirectURI, "temporarily_unavailable", q.Get("state"))
		return
	}

	browserCode, err := NewBrowserCode()
	if err != nil {
		redirectOAuthError(w, redirectURI, "server_error", q.Get("state"))
		return
	}
	userCode, err := p.NewUserCode(r.Context(), client.SessionID)
	if err != nil {
		redirectOAuthError(w, redirectURI, "temporarily_unavailable", q.Get("state"))
		return
	}
	now, exp := p.now(), p.now().Add(p.requestTTL)
	req := PendingRequest{
		ClientID: client.ClientID, OrganizationID: client.OrganizationID, SessionID: client.SessionID,
		RedirectURI: redirectURI, State: q.Get("state"), Nonce: q.Get("nonce"),
		CodeChallenge: q.Get("code_challenge"), CodeChallengeMethod: "S256", Scopes: scopes,
		Mode: mode, UserCode: userCode, BrowserCode: browserCode, LoginCommand: client.LoginCommand,
		AppName: client.Name, AppLogo: client.LogoURL, Target: p.target(r.Context(), mode, sess, client),
		Status: PendingStatusPending, CreatedAt: now.UnixMilli(), ExpiresAt: exp.UnixMilli(),
	}
	if err := p.pending.Create(r.Context(), req); err != nil {
		redirectOAuthError(w, redirectURI, "server_error", q.Get("state"))
		return
	}
	u := p.webLoginURL + "#c=" + url.QueryEscape(browserCode)
	http.Redirect(w, r, u, http.StatusFound)
}

func (p *Provider) HandleWaitStream(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "browser_code")
	req, err := p.pending.Load(r.Context(), code)
	if err != nil || req.Status == "" {
		http.NotFound(w, r)
		return
	}
	ip := clientIP(r)
	if !p.enterStream(code, ip) {
		http.Error(w, "too many streams", http.StatusTooManyRequests)
		return
	}
	defer p.leaveStream(code, ip)

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	writeFrame(w, snapshotFrame(req))
	flusher.Flush()
	if req.Status != PendingStatusPending {
		writeFrame(w, map[string]string{"status": req.Status})
		flusher.Flush()
		return
	}
	ctx, cancel := context.WithDeadline(r.Context(), time.UnixMilli(req.ExpiresAt))
	defer cancel()
	pubsub := p.pending.Subscribe(ctx, code)
	defer pubsub.Close()
	ch := pubsub.Channel()
	t := time.NewTicker(streamHeartbeat)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			_, _ = p.pending.Expire(context.Background(), code)
			writeFrame(w, map[string]string{"status": PendingStatusExpired})
			flusher.Flush()
			return
		case <-t.C:
			writeFrame(w, map[string]string{"status": "heartbeat"})
			flusher.Flush()
		case msg := <-ch:
			if msg == nil {
				return
			}
			if msg.Payload == PendingStatusVerified || msg.Payload == PendingStatusDenied || msg.Payload == PendingStatusExpired {
				writeFrame(w, map[string]string{"status": msg.Payload})
				flusher.Flush()
				return
			}
		}
	}
}

func (p *Provider) HandleCancel(w http.ResponseWriter, r *http.Request) {
	_, _ = p.pending.Cancel(r.Context(), chi.URLParam(r, "browser_code"))
	w.WriteHeader(http.StatusNoContent)
}

func snapshotFrame(req PendingRequest) map[string]any {
	app := map[string]any{"name": req.AppName, "logo": req.AppLogo}
	target := map[string]any{"mode": req.Target.Mode}
	if req.Target.Number != nil {
		target["number"] = *req.Target.Number
	}
	if req.Target.BotName != nil {
		target["bot_name"] = *req.Target.BotName
	}
	if req.Target.GroupName != nil {
		target["group_name"] = *req.Target.GroupName
	}
	return map[string]any{"status": "pending", "app": app, "user_code": req.UserCode, "login_command": req.LoginCommand, "target": target, "scopes": req.Scopes, "expires_at": req.ExpiresAt}
}

func writeFrame(w http.ResponseWriter, v any) {
	b, _ := json.Marshal(v)
	_, _ = w.Write(append(b, '\n'))
}

func (p *Provider) target(ctx context.Context, mode string, sess domain.WASession, c domain.OAuthClient) PendingTarget {
	var number, botName, groupName *string
	if sess.PhoneNumber != nil && *sess.PhoneNumber != "" {
		n := "+" + strings.TrimPrefix(*sess.PhoneNumber, "+")
		number = &n
	}
	if sess.Label != nil && *sess.Label != "" {
		botName = sess.Label
	}
	if mode == "group" && c.GroupJID != nil && p.groups != nil {
		if g, err := p.groups.GetByJID(ctx, *c.GroupJID); err == nil && g.Subject != nil && *g.Subject != "" {
			groupName = g.Subject
		}
		if groupName == nil {
			groupName = c.GroupJID
		}
	}
	return PendingTarget{Mode: mode, Number: number, BotName: botName, GroupName: groupName}
}

func validateAuthorizeBasics(q url.Values) error {
	if q.Get("response_type") != "code" {
		return fmt.Errorf("unsupported_response_type")
	}
	if q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
		return fmt.Errorf("invalid_request")
	}
	return nil
}

func containsJSON(raw json.RawMessage, needle string) bool {
	var xs []string
	_ = json.Unmarshal(raw, &xs)
	for _, x := range xs {
		if x == needle {
			return true
		}
	}
	return false
}

func resolveScopes(raw json.RawMessage, scope string) ([]string, error) {
	req := strings.Fields(scope)
	if len(req) == 0 {
		req = []string{"openid"}
	}
	allowed := map[string]bool{}
	var xs []string
	_ = json.Unmarshal(raw, &xs)
	for _, x := range xs {
		allowed[x] = true
	}
	for _, s := range req {
		if !allowed[s] {
			return nil, fmt.Errorf("invalid_scope")
		}
	}
	sort.Strings(req)
	return req, nil
}

func resolveMode(modes, acr string) (string, error) {
	have := map[string]bool{}
	for _, m := range strings.Split(modes, ",") {
		have[strings.TrimSpace(m)] = true
	}
	if acr == "" {
		if have["dm"] {
			return "dm", nil
		}
		if have["group"] {
			return "group", nil
		}
	}
	if strings.Contains(acr, "wa:dm") && have["dm"] {
		return "dm", nil
	}
	if strings.Contains(acr, "wa:group") && have["group"] {
		return "group", nil
	}
	return "", fmt.Errorf("invalid_request")
}

func NewBrowserCode() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (p *Provider) NewUserCode(ctx context.Context, sessionID string) (string, error) {
	for i := 0; i < 32; i++ {
		n, err := rand.Int(rand.Reader, big.NewInt(1000000))
		if err != nil {
			return "", err
		}
		code := fmt.Sprintf("%06d", n.Int64())
		if patternedUserCode(code) {
			continue
		}
		if exists, _ := p.pending.redis.Exists(ctx, p.pending.userCodeKey(sessionID, code)).Result(); exists == 0 {
			return code, nil
		}
	}
	return "", fmt.Errorf("user code collision")
}

func patternedUserCode(code string) bool {
	if len(code) != 6 {
		return true
	}
	allSame := true
	for i := 1; i < len(code); i++ {
		if code[i] != code[0] {
			allSame = false
			break
		}
	}
	if allSame {
		return true
	}
	switch code {
	case "012345", "123456", "234567", "345678", "456789", "987654", "876543", "765432", "654321":
		return true
	default:
		return false
	}
}

func redirectOAuthError(w http.ResponseWriter, redirectURI, code, state string) {
	u, _ := url.Parse(redirectURI)
	q := u.Query()
	q.Set("error", code)
	if state != "" {
		q.Set("state", state)
	}
	u.RawQuery = q.Encode()
	w.Header().Set("Location", u.String())
	w.WriteHeader(http.StatusFound)
}
func localOAuthError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, "<!doctype html><title>OAuth error</title><h1>%s</h1><p>%s</p>", code, msg)
}

func (p *Provider) enterStream(code, ip string) bool {
	p.streamMu.Lock()
	defer p.streamMu.Unlock()
	if p.byCode[code] >= 3 || p.byIP[ip] >= 30 {
		return false
	}
	p.byCode[code]++
	p.byIP[ip]++
	return true
}
func (p *Provider) leaveStream(code, ip string) {
	p.streamMu.Lock()
	defer p.streamMu.Unlock()
	p.byCode[code]--
	p.byIP[ip]--
}
func clientIP(r *http.Request) string {
	if x := r.Header.Get("X-Forwarded-For"); x != "" {
		return strings.TrimSpace(strings.Split(x, ",")[0])
	}
	return strings.Split(r.RemoteAddr, ":")[0]
}

var _ = redis.Nil
