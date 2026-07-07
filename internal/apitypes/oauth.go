package apitypes

type OAuthClientType string

const (
	OAuthClientConfidential OAuthClientType = "confidential"
	OAuthClientPublic       OAuthClientType = "public"
)

type OAuthAppStatus string

const (
	OAuthAppActive   OAuthAppStatus = "active"
	OAuthAppDisabled OAuthAppStatus = "disabled"
)

type OAuthAppMode string

const (
	OAuthAppModeDM    OAuthAppMode = "dm"
	OAuthAppModeGroup OAuthAppMode = "group"
)

type OAuthApp struct {
	ID                string          `json:"id" doc:"Internal OAuth application id. Use this id for dashboard management routes." example:"oac_01J9ZX8K2QHV0M3T6R7P4N5W8C"`
	ClientID          string          `json:"clientId" doc:"Public OAuth client_id used by relying applications in authorize and token requests." example:"wa_01J9ZX8K2QHV0M3T6R7P4N5W8C"`
	OrganizationID    string          `json:"organizationId" doc:"Organization that owns this OAuth application." example:"org_01J9ABC"`
	CreatedByUserID   *string         `json:"createdByUserId,omitempty" doc:"User id that created this OAuth application, when known." example:"user_01J9DEF"`
	SessionID         string          `json:"sessionId" doc:"WhatsApp session used as the Sign in with WhatsApp bot. Must belong to the owning organization." example:"sess_01J9ZX8K2QHV0M3T6R7P4N5W8C"`
	Name              string          `json:"name" doc:"Application name shown on the consent page and in bot replies." example:"Acme Portal"`
	LogoURL           *string         `json:"logoUrl,omitempty" doc:"Optional HTTPS logo URL shown on the consent page." example:"https://acme.example/logo.png"`
	ClientType        OAuthClientType `json:"clientType" enum:"confidential,public" doc:"OAuth client type. Confidential clients receive a client_secret shown once; public clients use PKCE only and have no secret." example:"confidential"`
	LoginCommand      string          `json:"loginCommand" doc:"Single-word command users type in WhatsApp before the six-digit code. Lowercase letters, digits, underscore, and hyphen only." example:"login"`
	SecretLast4       *string         `json:"secretLast4,omitempty" doc:"Last four characters of the current client secret. Null for public clients. The full secret is only returned on create or rotate." example:"W8xQ"`
	RedirectURIs      []string        `json:"redirectUris" doc:"Exact redirect URI allow-list. URIs must be absolute HTTPS URLs, except http://localhost and loopback are allowed for development; fragments are rejected." example:"[\"https://app.example.com/oauth/callback\"]"`
	Modes             []OAuthAppMode  `json:"modes" enum:"dm,group" doc:"Enabled WhatsApp verification modes. dm proves number control; group proves membership in the pinned group." example:"[\"dm\"]"`
	GroupJID          *string         `json:"groupJid,omitempty" doc:"Pinned WhatsApp group JID. Required when group mode is enabled and omitted otherwise." example:"120363025000000000@g.us"`
	AllowedScopes     []string        `json:"allowedScopes" doc:"OAuth/OIDC scopes this app may request." example:"[\"openid\",\"profile\",\"phone\"]"`
	TokenTTLSeconds   int             `json:"tokenTtlSeconds" doc:"Access-token and id-token lifetime in seconds." example:"900"`
	RefreshTTLSeconds int             `json:"refreshTtlSeconds" doc:"Refresh-token family maximum lifetime in seconds." example:"2592000"`
	Status            OAuthAppStatus  `json:"status" enum:"active,disabled" doc:"Whether new authorizations and token grants are accepted." example:"active"`
	CreatedAt         int64           `json:"createdAt" doc:"Creation time in epoch milliseconds." example:"1719662400000"`
	UpdatedAt         int64           `json:"updatedAt" doc:"Last update time in epoch milliseconds." example:"1719662400000"`
}

type OAuthAppWithSecret struct {
	OAuthApp
	ClientSecret string `json:"clientSecret,omitempty" doc:"Plaintext client secret. Returned exactly once on create or rotate for confidential clients; absent for public clients." example:"ows_6E9f9rT7s0z2H4nW8xQKpB1cD3mL5uV7yA9bC2dE4fG"`
}

type OAuthGrant struct {
	ID            string   `json:"id" doc:"Grant id." example:"ogr_01J9ZX8K2QHV0M3T6R7P4N5W8C"`
	ClientID      string   `json:"clientId" doc:"OAuth client_id this grant belongs to." example:"wa_01J9ZX8K2QHV0M3T6R7P4N5W8C"`
	WAIdentityID  uint64   `json:"waIdentityId" doc:"Internal WhatsApp identity row id." example:"1024"`
	Sub           string   `json:"sub" doc:"Pairwise subject issued to this client." example:"0Y0ONy3bczQk4e0SWO5wYywy7Egu5bBt2ukjBZIjtpc"`
	GrantedScopes []string `json:"grantedScopes" doc:"Scopes consented for this WhatsApp identity." example:"[\"openid\",\"profile\"]"`
	LastACR       string   `json:"lastAcr" enum:"wa:dm,wa:group" doc:"Last authentication context used for this grant." example:"wa:dm"`
	LastGroupJID  *string  `json:"lastGroupJid,omitempty" doc:"Group JID proven on the last group-mode login, when applicable." example:"120363025000000000@g.us"`
	CreatedAt     int64    `json:"createdAt" doc:"Grant creation time in epoch milliseconds." example:"1719662400000"`
	LastUsedAt    int64    `json:"lastUsedAt" doc:"Last successful use time in epoch milliseconds." example:"1719662400000"`
	RevokedAt     *int64   `json:"revokedAt,omitempty" doc:"Revocation time in epoch milliseconds. Omitted for active grants." example:"1719662400000"`
}
