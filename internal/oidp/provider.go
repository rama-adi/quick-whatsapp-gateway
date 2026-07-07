package oidp

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"
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
type IdentityReader interface {
	GetByLID(ctx context.Context, lid string) (domain.Identity, error)
	GetByID(ctx context.Context, id uint64) (domain.Identity, error)
}
type GrantRepo interface {
	UpsertAndGet(context.Context, domain.OAuthGrant) (domain.OAuthGrant, error)
	GetActiveByID(context.Context, string) (domain.OAuthGrant, error)
}
type RefreshRepo interface {
	Create(context.Context, domain.OAuthRefreshToken) error
	GetByHash(context.Context, []byte) (domain.OAuthRefreshToken, error)
	MarkConsumed(context.Context, string, int64) error
	RevokeFamily(context.Context, string, int64) error
	RevokeTokenHash(context.Context, []byte, int64) error
	RotateRefreshToken(context.Context, domain.OAuthRefreshRotation) (domain.OAuthRefreshToken, domain.OAuthGrant, error)
}

type ProviderConfig struct {
	Clients      OAuthClientReader
	Sessions     SessionReader
	Groups       GroupReader
	Identities   IdentityReader
	Grants       GrantRepo
	Refresh      RefreshRepo
	Signer       *Signer
	Pending      *PendingStore
	WebLoginURL  string
	Issuer       string
	SecretPepper string
	PairwiseSalt string
	RequestTTL   time.Duration
	AuthCodeTTL  time.Duration
	TrustProxy   bool
	Now          func() time.Time
}

type Provider struct {
	clients      OAuthClientReader
	sessions     SessionReader
	groups       GroupReader
	identities   IdentityReader
	grants       GrantRepo
	refresh      RefreshRepo
	signer       *Signer
	pending      *PendingStore
	webLoginURL  string
	issuer       string
	secretPepper string
	pairwiseSalt string
	requestTTL   time.Duration
	authCodeTTL  time.Duration
	trustProxy   bool
	now          func() time.Time
	streamMu     sync.Mutex
	byCode       map[string]int
	byIP         map[string]int
	revokedMu    sync.Mutex
	revokedGrant map[string]time.Time
}

func NewProvider(cfg ProviderConfig) *Provider {
	ttl := cfg.RequestTTL
	if ttl <= 0 {
		ttl = DefaultRequestTTL
	}
	authCodeTTL := cfg.AuthCodeTTL
	if authCodeTTL <= 0 {
		authCodeTTL = DefaultAuthCodeTTL
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Provider{clients: cfg.Clients, sessions: cfg.Sessions, groups: cfg.Groups, identities: cfg.Identities, grants: cfg.Grants, refresh: cfg.Refresh, signer: cfg.Signer, pending: cfg.Pending, webLoginURL: strings.TrimRight(cfg.WebLoginURL, "/"), issuer: strings.TrimRight(cfg.Issuer, "/"), secretPepper: cfg.SecretPepper, pairwiseSalt: cfg.PairwiseSalt, requestTTL: ttl, authCodeTTL: authCodeTTL, trustProxy: cfg.TrustProxy, now: now, byCode: map[string]int{}, byIP: map[string]int{}, revokedGrant: map[string]time.Time{}}
}

func (p *Provider) Mount(r chi.Router) {
	r.Get("/oauth/authorize", p.HandleAuthorize)
	r.Get("/oauth/wait/{browser_code}/stream", p.HandleWaitStream)
	r.Post("/oauth/wait/{browser_code}/finalize", p.HandleFinalize)
	r.Post("/oauth/wait/{browser_code}/cancel", p.HandleCancel)
	r.Post("/oauth/token", p.HandleToken)
	r.Get("/oauth/userinfo", p.HandleUserInfo)
	r.Post("/oauth/userinfo", p.HandleUserInfo)
	r.Post("/oauth/revoke", p.HandleRevoke)
}

func (p *Provider) PendingStore() *PendingStore {
	return p.pending
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
	exp := p.now().Add(p.requestTTL)
	userCode, err := p.NewUserCode(r.Context(), client.SessionID, exp.UnixMilli())
	if err != nil {
		redirectOAuthError(w, redirectURI, "temporarily_unavailable", q.Get("state"))
		return
	}
	now := p.now()
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
	ip := p.clientIP(r)
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

func (p *Provider) HandleFinalize(w http.ResponseWriter, r *http.Request) {
	browserCode := chi.URLParam(r, "browser_code")
	req, err := p.pending.Load(r.Context(), browserCode)
	if err != nil || req.Verified == nil || (req.Status != PendingStatusVerified && req.Status != PendingStatusFinalized) {
		http.NotFound(w, r)
		return
	}
	if req.Status == PendingStatusFinalized {
		if req.Finalized == nil {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"redirect": req.Finalized.Redirect})
		return
	}
	ident, err := p.identities.GetByLID(r.Context(), req.Verified.LID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	sub := p.pairwiseSub(req.ClientID, ident.ID)
	now := p.now().UnixMilli()
	scopes, _ := json.Marshal(req.Scopes)
	g, err := p.grants.UpsertAndGet(r.Context(), domain.OAuthGrant{
		ID: domain.NewOAuthGrantID(), OrganizationID: req.OrganizationID, ClientID: req.ClientID,
		WAIdentityID: ident.ID, Sub: sub, GrantedScopes: scopes, LastACR: "wa:" + req.Mode,
		LastGroupJID: req.Verified.GroupJID, CreatedAt: now, LastUsedAt: now,
	})
	if err != nil {
		oauthJSONError(w, http.StatusInternalServerError, "server_error")
		return
	}
	code, err := randomURLToken(32)
	if err != nil {
		oauthJSONError(w, http.StatusInternalServerError, "server_error")
		return
	}
	u, _ := url.Parse(req.RedirectURI)
	q := u.Query()
	q.Set("code", code)
	if req.State != "" {
		q.Set("state", req.State)
	}
	if p.issuer != "" {
		q.Set("iss", p.issuer)
	}
	u.RawQuery = q.Encode()
	req, ok, err := p.pending.Finalize(r.Context(), browserCode, FinalizedBlock{Code: code, Redirect: u.String()}, AuthCode{GrantID: g.ID, ClientID: req.ClientID, RedirectURI: req.RedirectURI, Scopes: req.Scopes, Nonce: req.Nonce, CodeChallenge: req.CodeChallenge, CodeChallengeMethod: req.CodeChallengeMethod, ACR: "wa:" + req.Mode, AuthTime: req.Verified.VerifiedAt / 1000}, p.authCodeTTL)
	if err != nil || !ok || req.Finalized == nil {
		oauthJSONError(w, http.StatusInternalServerError, "server_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"redirect": req.Finalized.Redirect})
}

func (p *Provider) HandleToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthJSONError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	client, err := p.authenticateClient(r)
	if err != nil {
		oauthJSONError(w, http.StatusUnauthorized, "invalid_client")
		return
	}
	switch r.Form.Get("grant_type") {
	case "authorization_code":
		p.tokenFromCode(w, r, client)
	case "refresh_token":
		p.tokenFromRefresh(w, r, client)
	default:
		oauthJSONError(w, http.StatusBadRequest, "unsupported_grant_type")
	}
}

func (p *Provider) tokenFromCode(w http.ResponseWriter, r *http.Request, client domain.OAuthClient) {
	ac, err := p.pending.RedeemAuthCode(r.Context(), r.Form.Get("code"))
	if err != nil || ac.ClientID != client.ClientID || ac.RedirectURI != r.Form.Get("redirect_uri") || ac.CodeChallengeMethod != "S256" || !verifyPKCE(ac.CodeChallenge, r.Form.Get("code_verifier")) {
		oauthJSONError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	g, err := p.grants.GetActiveByID(r.Context(), ac.GrantID)
	if err != nil || g.ClientID != client.ClientID {
		oauthJSONError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	tok, err := p.issueTokens(r.Context(), client, g, ac.Scopes, ac.Nonce, ac.ACR, ac.AuthTime, hasScope(ac.Scopes, "offline_access"), nil)
	if err != nil {
		oauthJSONError(w, http.StatusInternalServerError, "server_error")
		return
	}
	writeJSON(w, http.StatusOK, tok)
}

func (p *Provider) tokenFromRefresh(w http.ResponseWriter, r *http.Request, client domain.OAuthClient) {
	raw := r.Form.Get("refresh_token")
	now := p.now().UnixMilli()
	requestedScopes := []string(nil)
	if s := strings.TrimSpace(r.Form.Get("scope")); s != "" {
		requestedScopes = strings.Fields(s)
	}
	nextRaw, successor, err := p.newRefreshToken(client, domain.OAuthGrant{}, nil, nil)
	if err != nil {
		oauthJSONError(w, http.StatusInternalServerError, "server_error")
		return
	}
	rt, g, err := p.refresh.RotateRefreshToken(r.Context(), domain.OAuthRefreshRotation{
		TokenHash: shaBytes(raw), ClientID: client.ClientID, RequestedScopes: requestedScopes, Now: now, Successor: successor,
	})
	if err != nil {
		if errors.Is(err, domain.ErrOAuthScopeWidening) {
			oauthJSONError(w, http.StatusBadRequest, "invalid_scope")
			return
		}
		oauthJSONError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	if p.isGrantRevoked(rt.GrantID) {
		_ = p.refresh.RevokeFamily(r.Context(), rt.FamilyID, now)
		oauthJSONError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	if checked, err := p.grants.GetActiveByID(r.Context(), rt.GrantID); err != nil || checked.ClientID != client.ClientID {
		_ = p.refresh.RevokeFamily(r.Context(), rt.FamilyID, now)
		oauthJSONError(w, http.StatusBadRequest, "invalid_grant")
		return
	} else {
		g = checked
	}
	oldScopes, _ := scopesFromRaw(rt.Scopes)
	nextScopes := oldScopes
	if len(requestedScopes) > 0 {
		nextScopes = requestedScopes
	}
	tok, err := p.issueTokens(r.Context(), client, g, nextScopes, "", g.LastACR, now/1000, false, nil)
	if err != nil {
		oauthJSONError(w, http.StatusInternalServerError, "server_error")
		return
	}
	tok["refresh_token"] = nextRaw
	writeJSON(w, http.StatusOK, tok)
}

func (p *Provider) MarkGrantRevoked(grantID string) {
	if grantID == "" {
		return
	}
	p.revokedMu.Lock()
	defer p.revokedMu.Unlock()
	cutoff := p.now().Add(-DefaultRequestTTL)
	for id, ts := range p.revokedGrant {
		if ts.Before(cutoff) {
			delete(p.revokedGrant, id)
		}
	}
	p.revokedGrant[grantID] = p.now()
}

func (p *Provider) InvalidateSession(sessionID string) {
	if p.pending == nil || sessionID == "" {
		return
	}
	_ = p.pending.ExpireBySession(context.Background(), sessionID)
}

func (p *Provider) isGrantRevoked(grantID string) bool {
	p.revokedMu.Lock()
	defer p.revokedMu.Unlock()
	ts, ok := p.revokedGrant[grantID]
	if !ok {
		return false
	}
	if p.now().Sub(ts) > DefaultRequestTTL {
		delete(p.revokedGrant, grantID)
		return false
	}
	return true
}

func (p *Provider) IsGrantRevoked(grantID string) bool {
	return p.isGrantRevoked(grantID)
}

func (p *Provider) issueTokens(ctx context.Context, client domain.OAuthClient, g domain.OAuthGrant, scopes []string, nonce, acr string, authTime int64, withRefresh bool, parent *domain.OAuthRefreshToken) (map[string]any, error) {
	ident, err := p.identityByGrant(ctx, g)
	if err != nil {
		return nil, err
	}
	now := p.now()
	iat, exp := now.Unix(), now.Add(time.Duration(client.TokenTTLSeconds)*time.Second).Unix()
	idClaims := p.identityClaims(ctx, scopes, ident, g)
	idClaims["iss"], idClaims["aud"], idClaims["sub"], idClaims["iat"], idClaims["exp"] = p.issuer, client.ClientID, g.Sub, iat, exp
	idClaims["acr"], idClaims["amr"], idClaims["auth_time"] = acr, []string{"whatsapp"}, authTime
	if nonce != "" {
		idClaims["nonce"] = nonce
	}
	idToken, err := p.signer.SignJWT(ctx, idClaims)
	if err != nil {
		return nil, err
	}
	jti := domain.NewULID()
	atClaims := map[string]any{"iss": p.issuer, "aud": client.ClientID, "sub": g.Sub, "iat": iat, "exp": exp, "scope": strings.Join(scopes, " "), "client_id": client.ClientID, "grant_id": g.ID, "jti": jti, "org_id": client.OrganizationID, "session_id": client.SessionID, "typ": "access"}
	accessToken, err := p.signer.SignJWT(ctx, atClaims)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"token_type": "Bearer", "expires_in": client.TokenTTLSeconds, "id_token": idToken, "access_token": accessToken, "scope": strings.Join(scopes, " ")}
	if withRefresh {
		rt, err := p.createRefresh(ctx, client, g, scopes, parent)
		if err != nil {
			return nil, err
		}
		out["refresh_token"] = rt
	}
	return out, nil
}

func (p *Provider) createRefresh(ctx context.Context, client domain.OAuthClient, g domain.OAuthGrant, scopes []string, parent *domain.OAuthRefreshToken) (string, error) {
	raw, rt, err := p.newRefreshToken(client, g, scopes, parent)
	if err != nil {
		return "", err
	}
	return raw, p.refresh.Create(ctx, rt)
}

func (p *Provider) newRefreshToken(client domain.OAuthClient, g domain.OAuthGrant, scopes []string, parent *domain.OAuthRefreshToken) (string, domain.OAuthRefreshToken, error) {
	id, err := randomURLToken(16)
	if err != nil {
		return "", domain.OAuthRefreshToken{}, err
	}
	secret, err := randomURLToken(32)
	if err != nil {
		return "", domain.OAuthRefreshToken{}, err
	}
	raw := id + "." + secret
	now := p.now().UnixMilli()
	fam := domain.NewULID()
	var parentID *string
	expires := now + int64(client.RefreshTTLSeconds)*1000
	if parent != nil {
		fam = parent.FamilyID
		parentID = &parent.ID
		expires = parent.ExpiresAt
	}
	scopesJSON, _ := json.Marshal(scopes)
	return raw, domain.OAuthRefreshToken{ID: domain.NewULID(), GrantID: g.ID, OrganizationID: g.OrganizationID, TokenHash: shaBytes(raw), FamilyID: fam, ParentID: parentID, Scopes: scopesJSON, IssuedAt: now, ExpiresAt: expires}, nil
}

func (p *Provider) HandleUserInfo(w http.ResponseWriter, r *http.Request) {
	claims, err := p.verifyAccessToken(r)
	if err != nil {
		oauthJSONError(w, http.StatusUnauthorized, "invalid_token")
		return
	}
	g, err := p.grants.GetActiveByID(r.Context(), fmt.Sprint(claims["grant_id"]))
	if err != nil {
		// Older access token shape does not carry grant_id; resolve by subject is intentionally unsupported.
		oauthJSONError(w, http.StatusUnauthorized, "invalid_token")
		return
	}
	ident, err := p.identityByGrant(r.Context(), g)
	if err != nil {
		oauthJSONError(w, http.StatusUnauthorized, "invalid_token")
		return
	}
	scopes := strings.Fields(fmt.Sprint(claims["scope"]))
	out := p.identityClaims(r.Context(), scopes, ident, g)
	out["sub"] = g.Sub
	writeJSON(w, http.StatusOK, out)
}

func (p *Provider) HandleRevoke(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	client, err := p.authenticateClient(r)
	if err != nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	rt, err := p.refresh.GetByHash(r.Context(), shaBytes(r.Form.Get("token")))
	if err == nil {
		if g, gerr := p.grants.GetActiveByID(r.Context(), rt.GrantID); gerr == nil && g.ClientID == client.ClientID {
			_ = p.refresh.RevokeFamily(r.Context(), rt.FamilyID, p.now().UnixMilli())
		}
	}
	w.WriteHeader(http.StatusOK)
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

func (p *Provider) authenticateClient(r *http.Request) (domain.OAuthClient, error) {
	basicID, basicSecret, hasBasic := r.BasicAuth()
	formID, formSecret := r.Form.Get("client_id"), r.Form.Get("client_secret")
	if hasBasic && (formID != "" || formSecret != "") {
		return domain.OAuthClient{}, errors.New("conflicting client credentials")
	}
	clientID := formID
	if hasBasic {
		clientID = basicID
	}
	c, err := p.clients.GetActiveByClientID(r.Context(), clientID)
	if err != nil {
		return domain.OAuthClient{}, err
	}
	if c.ClientType == "public" {
		if hasBasic || formSecret != "" {
			return domain.OAuthClient{}, errors.New("public client secret not accepted")
		}
		return c, nil
	}
	secret := formSecret
	if hasBasic {
		secret = basicSecret
	}
	if secret == "" || subtle.ConstantTimeCompare(shaSecret(secret, p.secretPepper), c.SecretHash) != 1 {
		return domain.OAuthClient{}, errors.New("invalid client secret")
	}
	return c, nil
}

func (p *Provider) pairwiseSub(clientID string, identityID uint64) string {
	mac := hmac.New(sha256.New, []byte(p.pairwiseSalt))
	_, _ = mac.Write([]byte(fmt.Sprintf("v1:%s:%d", clientID, identityID)))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (p *Provider) identityByGrant(ctx context.Context, g domain.OAuthGrant) (domain.Identity, error) {
	return p.identities.GetByID(ctx, g.WAIdentityID)
}

func (p *Provider) identityClaims(ctx context.Context, scopes []string, ident domain.Identity, g domain.OAuthGrant) map[string]any {
	out := map[string]any{}
	if hasScope(scopes, "profile") {
		if ident.Name != nil && *ident.Name != "" {
			out["name"] = *ident.Name
		} else if ident.BusinessName != nil && *ident.BusinessName != "" {
			out["name"] = *ident.BusinessName
		}
	}
	if hasScope(scopes, "phone") && ident.PhoneNumber != nil && *ident.PhoneNumber != "" {
		out["phone_number"] = *ident.PhoneNumber
		out["phone_number_verified"] = true
		if ident.PhoneJID != nil && *ident.PhoneJID != "" {
			out["wa_jid"] = *ident.PhoneJID
		}
	}
	if hasScope(scopes, "wa:group") && g.LastACR == "wa:group" && g.LastGroupJID != nil && *g.LastGroupJID != "" {
		out["wa_group_verified"] = true
		out["wa_group_id"] = *g.LastGroupJID
		if p.groups != nil {
			if group, err := p.groups.GetByJID(ctx, *g.LastGroupJID); err == nil && group.Subject != nil && *group.Subject != "" {
				out["wa_group_name"] = *group.Subject
			}
		}
	}
	return out
}

func (p *Provider) verifyAccessToken(r *http.Request) (map[string]any, error) {
	raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if raw == "" || raw == r.Header.Get("Authorization") {
		return nil, errors.New("missing bearer")
	}
	jwks, err := p.signer.JWKS(r.Context())
	if err != nil {
		return nil, err
	}
	set, err := jwk.Parse(jwks)
	if err != nil {
		return nil, err
	}
	tok, err := jwt.Parse([]byte(raw), jwt.WithKeySet(set), jwt.WithValidate(true), jwt.WithIssuer(p.issuer), jwt.WithClock(jwt.ClockFunc(p.now)))
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if sub, ok := tok.Subject(); ok {
		out["sub"] = sub
	}
	var scope, grantID, typ string
	_ = tok.Get("scope", &scope)
	_ = tok.Get("grant_id", &grantID)
	_ = tok.Get("typ", &typ)
	if typ != "access" {
		return nil, errors.New("not an access token")
	}
	out["scope"] = scope
	out["grant_id"] = grantID
	return out, nil
}

func verifyPKCE(challenge, verifier string) bool {
	if challenge == "" || verifier == "" {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	return subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(challenge)) == 1
}

func shaSecret(secret, pepper string) []byte {
	sum := sha256.Sum256([]byte(secret + pepper))
	return sum[:]
}

func shaBytes(v string) []byte {
	sum := sha256.Sum256([]byte(v))
	return sum[:]
}

func hasScope(scopes []string, scope string) bool {
	for _, s := range scopes {
		if s == scope {
			return true
		}
	}
	return false
}

func scopeSubset(next, base []string) bool {
	have := map[string]bool{}
	for _, s := range base {
		have[s] = true
	}
	for _, s := range next {
		if !have[s] {
			return false
		}
	}
	return true
}

func scopesFromRaw(raw json.RawMessage) ([]string, error) {
	var out []string
	return out, json.Unmarshal(raw, &out)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func oauthJSONError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
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
	tokens := map[string]bool{}
	for _, tok := range strings.Fields(acr) {
		tokens[tok] = true
	}
	if tokens["wa:group"] && have["group"] {
		return "group", nil
	}
	if tokens["wa:dm"] && have["dm"] {
		return "dm", nil
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

func (p *Provider) NewUserCode(ctx context.Context, sessionID string, expiresAt int64) (string, error) {
	for i := 0; i < 32; i++ {
		n, err := rand.Int(rand.Reader, big.NewInt(1000000))
		if err != nil {
			return "", err
		}
		code := fmt.Sprintf("%06d", n.Int64())
		if patternedUserCode(code) {
			continue
		}
		ok, err := p.pending.ReserveUserCode(ctx, sessionID, code, expiresAt)
		if err != nil {
			return "", err
		}
		if ok {
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
func (p *Provider) clientIP(r *http.Request) string {
	if p.trustProxy {
		if x := r.Header.Get("X-Forwarded-For"); x != "" {
			return strings.TrimSpace(strings.Split(x, ",")[0])
		}
	}
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		host = h
	}
	if host == "" {
		return r.RemoteAddr
	}
	return host
}

var _ = redis.Nil
