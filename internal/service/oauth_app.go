package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
)

const (
	defaultOAuthLoginCommand      = "login"
	defaultOAuthTokenTTLSeconds   = 900
	defaultOAuthRefreshTTLSeconds = 2592000
)

var oauthLoginCommandRE = regexp.MustCompile(`^[a-z0-9_-]{2,32}$`)

type OAuthAppService struct {
	clients      *store.OAuthClientRepo
	grants       *store.OAuthGrantRepo
	refresh      *store.OAuthRefreshTokenRepo
	sessions     *store.SessionRepo
	secretPepper string
	adminPrefix  string
}

func NewOAuthAppService(st *store.Store, secretPepper, adminPrefix string) *OAuthAppService {
	if secretPepper == "" {
		secretPepper = os.Getenv("OAUTH_CLIENT_SECRET_PEPPER")
	}
	if adminPrefix == "" {
		adminPrefix = os.Getenv("WHATSAPP_ADMIN_CMD_PREFIX")
	}
	return &OAuthAppService{
		clients:      st.OAuthClients,
		grants:       st.OAuthGrants,
		refresh:      st.OAuthRefresh,
		sessions:     st.Sessions,
		secretPepper: secretPepper,
		adminPrefix:  strings.ToLower(adminPrefix),
	}
}

type OAuthAppCreateInput struct {
	SessionID         string
	Name              string
	LogoURL           *string
	ClientType        string
	LoginCommand      string
	RedirectURIs      []string
	Modes             []string
	GroupJID          *string
	AllowedScopes     []string
	TokenTTLSeconds   int
	RefreshTTLSeconds int
	CreatedByUserID   *string
}

type OAuthAppUpdateInput struct {
	SessionID         *string
	Name              *string
	LogoURL           **string
	ClientType        *string
	LoginCommand      *string
	RedirectURIs      *[]string
	Modes             *[]string
	GroupJID          **string
	AllowedScopes     *[]string
	TokenTTLSeconds   *int
	RefreshTTLSeconds *int
}

func (s *OAuthAppService) List(ctx context.Context, org string, isSuperAdmin bool, cursor string, limit int) (store.Page[apitypes.OAuthApp], error) {
	if isSuperAdmin {
		// The repo has no cross-org list because the dashboard is org-scoped; super_admin
		// bypass applies to direct id lookups.
	}
	page, err := s.clients.ListByOrg(ctx, org, cursor, limit)
	if err != nil {
		return store.Page[apitypes.OAuthApp]{}, err
	}
	items, err := mapOAuthClients(page.Items)
	if err != nil {
		return store.Page[apitypes.OAuthApp]{}, err
	}
	return store.Page[apitypes.OAuthApp]{Items: items, NextCursor: page.NextCursor}, nil
}

func (s *OAuthAppService) Create(ctx context.Context, org string, in OAuthAppCreateInput) (apitypes.OAuthAppWithSecret, error) {
	if err := s.assertSessionOrg(ctx, org, in.SessionID); err != nil {
		return apitypes.OAuthAppWithSecret{}, err
	}
	now := domain.NowMs()
	clientType := in.ClientType
	if clientType == "" {
		clientType = string(apitypes.OAuthClientConfidential)
	}
	loginCommand := in.LoginCommand
	if loginCommand == "" {
		loginCommand = defaultOAuthLoginCommand
	}
	tokenTTL := in.TokenTTLSeconds
	if tokenTTL == 0 {
		tokenTTL = defaultOAuthTokenTTLSeconds
	}
	refreshTTL := in.RefreshTTLSeconds
	if refreshTTL == 0 {
		refreshTTL = defaultOAuthRefreshTTLSeconds
	}
	c := domain.OAuthClient{
		ID:                domain.NewOAuthClientID(),
		ClientID:          "wa_" + domain.NewULID(),
		OrganizationID:    org,
		CreatedByUserID:   in.CreatedByUserID,
		SessionID:         in.SessionID,
		Name:              strings.TrimSpace(in.Name),
		LogoURL:           trimStringPtr(in.LogoURL),
		ClientType:        clientType,
		LoginCommand:      strings.TrimSpace(loginCommand),
		GroupJID:          trimStringPtr(in.GroupJID),
		TokenTTLSeconds:   tokenTTL,
		RefreshTTLSeconds: refreshTTL,
		Status:            string(apitypes.OAuthAppActive),
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	var secret string
	if err := s.validateAndFill(&c, in.RedirectURIs, in.Modes, in.AllowedScopes); err != nil {
		return apitypes.OAuthAppWithSecret{}, err
	}
	if c.ClientType == string(apitypes.OAuthClientConfidential) {
		var err error
		secret, err = randomSecret()
		if err != nil {
			return apitypes.OAuthAppWithSecret{}, err
		}
		c.SecretHash = s.hashSecret(secret)
		last4 := secret[len(secret)-4:]
		c.SecretLast4 = &last4
	}
	if err := s.clients.Create(ctx, c); err != nil {
		return apitypes.OAuthAppWithSecret{}, err
	}
	dto, err := mapOAuthClient(c)
	if err != nil {
		return apitypes.OAuthAppWithSecret{}, err
	}
	return apitypes.OAuthAppWithSecret{OAuthApp: dto, ClientSecret: secret}, nil
}

func (s *OAuthAppService) Get(ctx context.Context, org, id string, isSuperAdmin bool) (apitypes.OAuthApp, error) {
	c, err := s.getClient(ctx, org, id, isSuperAdmin)
	if err != nil {
		return apitypes.OAuthApp{}, err
	}
	return mapOAuthClient(c)
}

func (s *OAuthAppService) Update(ctx context.Context, org, id string, isSuperAdmin bool, in OAuthAppUpdateInput) (apitypes.OAuthApp, error) {
	c, err := s.getClient(ctx, org, id, isSuperAdmin)
	if err != nil {
		return apitypes.OAuthApp{}, err
	}
	if in.SessionID != nil {
		if err := s.assertSessionOrg(ctx, c.OrganizationID, *in.SessionID); err != nil {
			return apitypes.OAuthApp{}, err
		}
		c.SessionID = strings.TrimSpace(*in.SessionID)
	}
	if in.Name != nil {
		c.Name = strings.TrimSpace(*in.Name)
	}
	if in.LogoURL != nil {
		c.LogoURL = trimStringPtr(*in.LogoURL)
	}
	if in.ClientType != nil {
		next := strings.TrimSpace(*in.ClientType)
		if next != c.ClientType {
			if next == string(apitypes.OAuthClientPublic) {
				c.ClientType = next
				c.SecretHash = nil
				c.SecretLast4 = nil
			} else {
				return apitypes.OAuthApp{}, domain.ErrValidation("clientType can only be changed from confidential to public; create or rotate a confidential secret explicitly")
			}
		}
	}
	if in.LoginCommand != nil {
		c.LoginCommand = strings.TrimSpace(*in.LoginCommand)
	}
	redirects, err := stringSliceFromRaw(c.RedirectURIs)
	if err != nil {
		return apitypes.OAuthApp{}, err
	}
	if in.RedirectURIs != nil {
		redirects = *in.RedirectURIs
	}
	modes := strings.Split(c.Modes, ",")
	if in.Modes != nil {
		modes = *in.Modes
	}
	scopes, err := stringSliceFromRaw(c.AllowedScopes)
	if err != nil {
		return apitypes.OAuthApp{}, err
	}
	if in.AllowedScopes != nil {
		scopes = *in.AllowedScopes
	}
	if in.GroupJID != nil {
		c.GroupJID = trimStringPtr(*in.GroupJID)
	}
	if in.TokenTTLSeconds != nil {
		c.TokenTTLSeconds = *in.TokenTTLSeconds
	}
	if in.RefreshTTLSeconds != nil {
		c.RefreshTTLSeconds = *in.RefreshTTLSeconds
	}
	c.UpdatedAt = domain.NowMs()
	if err := s.validateAndFill(&c, redirects, modes, scopes); err != nil {
		return apitypes.OAuthApp{}, err
	}
	if err := s.clients.Update(ctx, c); err != nil {
		return apitypes.OAuthApp{}, err
	}
	return mapOAuthClient(c)
}

func (s *OAuthAppService) RotateSecret(ctx context.Context, org, id string, isSuperAdmin bool) (apitypes.OAuthAppWithSecret, error) {
	c, err := s.getClient(ctx, org, id, isSuperAdmin)
	if err != nil {
		return apitypes.OAuthAppWithSecret{}, err
	}
	if c.ClientType == string(apitypes.OAuthClientPublic) {
		return apitypes.OAuthAppWithSecret{}, domain.ErrValidation("public clients do not have a client_secret")
	}
	secret, err := randomSecret()
	if err != nil {
		return apitypes.OAuthAppWithSecret{}, err
	}
	c.SecretHash = s.hashSecret(secret)
	last4 := secret[len(secret)-4:]
	c.SecretLast4 = &last4
	c.UpdatedAt = domain.NowMs()
	if err := s.clients.Update(ctx, c); err != nil {
		return apitypes.OAuthAppWithSecret{}, err
	}
	dto, err := mapOAuthClient(c)
	if err != nil {
		return apitypes.OAuthAppWithSecret{}, err
	}
	return apitypes.OAuthAppWithSecret{OAuthApp: dto, ClientSecret: secret}, nil
}

func (s *OAuthAppService) Delete(ctx context.Context, org, id string, isSuperAdmin bool) error {
	c, err := s.getClient(ctx, org, id, isSuperAdmin)
	if err != nil {
		return err
	}
	now := domain.NowMs()
	if err := s.refresh.RevokeByClient(ctx, c.OrganizationID, c.ClientID, now); err != nil {
		return err
	}
	if err := s.grants.RevokeByClient(ctx, c.OrganizationID, c.ClientID, now); err != nil {
		return err
	}
	return s.clients.SoftDelete(ctx, c.OrganizationID, c.ID, now)
}

func (s *OAuthAppService) SetEnabled(ctx context.Context, org, id string, isSuperAdmin bool, enabled bool) (apitypes.OAuthApp, error) {
	c, err := s.getClient(ctx, org, id, isSuperAdmin)
	if err != nil {
		return apitypes.OAuthApp{}, err
	}
	if enabled {
		c.Status = string(apitypes.OAuthAppActive)
	} else {
		c.Status = string(apitypes.OAuthAppDisabled)
	}
	c.UpdatedAt = domain.NowMs()
	if err := s.clients.Update(ctx, c); err != nil {
		return apitypes.OAuthApp{}, err
	}
	return mapOAuthClient(c)
}

func (s *OAuthAppService) ListGrants(ctx context.Context, org, appID string, isSuperAdmin bool, cursor string, limit int) (store.Page[apitypes.OAuthGrant], error) {
	c, err := s.getClient(ctx, org, appID, isSuperAdmin)
	if err != nil {
		return store.Page[apitypes.OAuthGrant]{}, err
	}
	page, err := s.grants.ListByClient(ctx, c.OrganizationID, c.ClientID, cursor, limit)
	if err != nil {
		return store.Page[apitypes.OAuthGrant]{}, err
	}
	items, err := mapOAuthGrants(page.Items)
	if err != nil {
		return store.Page[apitypes.OAuthGrant]{}, err
	}
	return store.Page[apitypes.OAuthGrant]{Items: items, NextCursor: page.NextCursor}, nil
}

func (s *OAuthAppService) RevokeGrant(ctx context.Context, org, appID, grantID string, isSuperAdmin bool) error {
	c, err := s.getClient(ctx, org, appID, isSuperAdmin)
	if err != nil {
		return err
	}
	g, err := s.grants.GetByOrg(ctx, c.OrganizationID, grantID)
	if err != nil {
		return err
	}
	if g.ClientID != c.ClientID {
		return domain.ErrNotFound("oauth grant not found")
	}
	now := domain.NowMs()
	if err := s.refresh.RevokeByGrant(ctx, c.OrganizationID, grantID, now); err != nil {
		return err
	}
	return s.grants.Revoke(ctx, c.OrganizationID, grantID, now)
}

func (s *OAuthAppService) getClient(ctx context.Context, org, id string, isSuperAdmin bool) (domain.OAuthClient, error) {
	if isSuperAdmin {
		return s.clients.GetAny(ctx, id)
	}
	return s.clients.GetByOrg(ctx, org, id)
}

func (s *OAuthAppService) assertSessionOrg(ctx context.Context, org, id string) error {
	sess, err := s.sessions.Get(ctx, strings.TrimSpace(id))
	if err != nil {
		return err
	}
	if sess.OrganizationID != org {
		return domain.ErrNotFound("session not found")
	}
	return nil
}

func (s *OAuthAppService) validateAndFill(c *domain.OAuthClient, redirects, modes, scopes []string) error {
	if c.Name == "" {
		return domain.ErrValidation("name is required")
	}
	if c.ClientType != string(apitypes.OAuthClientConfidential) && c.ClientType != string(apitypes.OAuthClientPublic) {
		return domain.ErrValidation("clientType must be confidential or public")
	}
	if !oauthLoginCommandRE.MatchString(c.LoginCommand) {
		return domain.ErrValidation("loginCommand must match [a-z0-9_-]{2,32}")
	}
	if s.adminPrefix != "" && strings.EqualFold(c.LoginCommand, s.adminPrefix) {
		return domain.ErrValidation("loginCommand must not collide with WHATSAPP_ADMIN_CMD_PREFIX")
	}
	normalizedRedirects, err := validateRedirectURIs(redirects)
	if err != nil {
		return err
	}
	c.RedirectURIs, _ = json.Marshal(normalizedRedirects)
	normalizedModes, err := validateModes(modes)
	if err != nil {
		return err
	}
	c.Modes = strings.Join(normalizedModes, ",")
	hasGroup := false
	for _, m := range normalizedModes {
		hasGroup = hasGroup || m == string(apitypes.OAuthAppModeGroup)
	}
	if hasGroup && (c.GroupJID == nil || strings.TrimSpace(*c.GroupJID) == "") {
		return domain.ErrValidation("groupJid is required when group mode is enabled")
	}
	if !hasGroup && c.GroupJID != nil {
		return domain.ErrValidation("groupJid is only allowed when group mode is enabled")
	}
	normalizedScopes := normalizeStringSet(scopes)
	if len(normalizedScopes) == 0 {
		normalizedScopes = []string{"openid", "profile"}
	}
	c.AllowedScopes, _ = json.Marshal(normalizedScopes)
	if c.TokenTTLSeconds <= 0 {
		return domain.ErrValidation("tokenTtlSeconds must be positive")
	}
	if c.RefreshTTLSeconds <= 0 {
		return domain.ErrValidation("refreshTtlSeconds must be positive")
	}
	return nil
}

func validateRedirectURIs(values []string) ([]string, error) {
	values = normalizeStringSet(values)
	if len(values) == 0 {
		return nil, domain.ErrValidation("redirectUris is required")
	}
	for _, raw := range values {
		u, err := url.Parse(raw)
		if err != nil || !u.IsAbs() || u.Host == "" {
			return nil, domain.ErrValidation("redirectUris must be absolute URLs")
		}
		if u.Fragment != "" {
			return nil, domain.ErrValidation("redirectUris must not include fragments")
		}
		if u.Scheme == "https" {
			continue
		}
		if u.Scheme == "http" && isLocalhost(u.Hostname()) {
			continue
		}
		return nil, domain.ErrValidation("redirectUris must use https except localhost http for development")
	}
	return values, nil
}

func validateModes(values []string) ([]string, error) {
	values = normalizeStringSet(values)
	if len(values) == 0 {
		return []string{string(apitypes.OAuthAppModeDM)}, nil
	}
	for _, v := range values {
		if v != string(apitypes.OAuthAppModeDM) && v != string(apitypes.OAuthAppModeGroup) {
			return nil, domain.ErrValidation("modes must contain only dm or group")
		}
	}
	return values, nil
}

func isLocalhost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func normalizeStringSet(values []string) []string {
	set := map[string]struct{}{}
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v != "" {
			set[v] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func trimStringPtr(v *string) *string {
	if v == nil {
		return nil
	}
	s := strings.TrimSpace(*v)
	if s == "" {
		return nil
	}
	return &s
}

func stringSliceFromRaw(raw json.RawMessage) ([]string, error) {
	var out []string
	if len(raw) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, domain.ErrInternal("invalid oauth client JSON state")
	}
	return out, nil
}

func mapOAuthClients(in []domain.OAuthClient) ([]apitypes.OAuthApp, error) {
	out := make([]apitypes.OAuthApp, 0, len(in))
	for _, c := range in {
		dto, err := mapOAuthClient(c)
		if err != nil {
			return nil, err
		}
		out = append(out, dto)
	}
	return out, nil
}

func mapOAuthClient(c domain.OAuthClient) (apitypes.OAuthApp, error) {
	redirects, err := stringSliceFromRaw(c.RedirectURIs)
	if err != nil {
		return apitypes.OAuthApp{}, err
	}
	scopes, err := stringSliceFromRaw(c.AllowedScopes)
	if err != nil {
		return apitypes.OAuthApp{}, err
	}
	modes := []apitypes.OAuthAppMode{}
	for _, m := range strings.Split(c.Modes, ",") {
		m = strings.TrimSpace(m)
		if m != "" {
			modes = append(modes, apitypes.OAuthAppMode(m))
		}
	}
	return apitypes.OAuthApp{
		ID:                c.ID,
		ClientID:          c.ClientID,
		OrganizationID:    c.OrganizationID,
		CreatedByUserID:   c.CreatedByUserID,
		SessionID:         c.SessionID,
		Name:              c.Name,
		LogoURL:           c.LogoURL,
		ClientType:        apitypes.OAuthClientType(c.ClientType),
		LoginCommand:      c.LoginCommand,
		SecretLast4:       c.SecretLast4,
		RedirectURIs:      redirects,
		Modes:             modes,
		GroupJID:          c.GroupJID,
		AllowedScopes:     scopes,
		TokenTTLSeconds:   c.TokenTTLSeconds,
		RefreshTTLSeconds: c.RefreshTTLSeconds,
		Status:            apitypes.OAuthAppStatus(c.Status),
		CreatedAt:         c.CreatedAt,
		UpdatedAt:         c.UpdatedAt,
	}, nil
}

func mapOAuthGrants(in []domain.OAuthGrant) ([]apitypes.OAuthGrant, error) {
	out := make([]apitypes.OAuthGrant, 0, len(in))
	for _, g := range in {
		scopes, err := stringSliceFromRaw(g.GrantedScopes)
		if err != nil {
			return nil, err
		}
		out = append(out, apitypes.OAuthGrant{
			ID:            g.ID,
			ClientID:      g.ClientID,
			WAIdentityID:  g.WAIdentityID,
			Sub:           g.Sub,
			GrantedScopes: scopes,
			LastACR:       g.LastACR,
			LastGroupJID:  g.LastGroupJID,
			CreatedAt:     g.CreatedAt,
			LastUsedAt:    g.LastUsedAt,
			RevokedAt:     g.RevokedAt,
		})
	}
	return out, nil
}

func randomSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "ows_" + base64.RawURLEncoding.EncodeToString(b), nil
}

func (s *OAuthAppService) hashSecret(secret string) []byte {
	sum := sha256.Sum256([]byte(secret + s.secretPepper))
	return sum[:]
}

func VerifyOAuthClientSecret(secret, pepper string, hash []byte) bool {
	sum := sha256.Sum256([]byte(secret + pepper))
	return subtle.ConstantTimeCompare(sum[:], hash) == 1
}
