package handlers

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/apitypes"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/authz"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/humax"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/service"
)

type oauthAppBody struct {
	SessionID         string                  `json:"sessionId,omitempty" doc:"WhatsApp session used as the Sign in with WhatsApp bot. Must belong to the caller's organization." example:"sess_01J9ZX8K2QHV0M3T6R7P4N5W8C"`
	Name              string                  `json:"name,omitempty" doc:"Application name shown on the consent page and in WhatsApp bot replies." example:"Acme Portal"`
	LogoURL           *string                 `json:"logoUrl,omitempty" doc:"Optional HTTPS logo URL shown on the consent page. Send null or omit to leave unset." example:"https://acme.example/logo.png"`
	ClientType        string                  `json:"clientType,omitempty" enum:"confidential,public" doc:"OAuth client type. Defaults to confidential. Confidential clients receive a client_secret shown once; public clients use PKCE only." example:"confidential"`
	LoginCommand      string                  `json:"loginCommand,omitempty" doc:"Single-word command users type in WhatsApp before the six-digit code. Must match [a-z0-9_-]{2,32} and must not equal WHATSAPP_ADMIN_CMD_PREFIX." example:"login"`
	RedirectURIs      []string                `json:"redirectUris,omitempty" doc:"Exact redirect URI set. Absolute HTTPS only, except http://localhost and loopback are allowed for development; fragments are rejected." example:"[\"https://app.example.com/oauth/callback\"]"`
	Modes             []apitypes.OAuthAppMode `json:"modes,omitempty" enum:"dm,group" doc:"Verification modes to enable. Defaults to [\"dm\"]. If group is included, groupJid is required." example:"[\"dm\"]"`
	GroupJID          *string                 `json:"groupJid,omitempty" doc:"Pinned WhatsApp group JID. Required iff group mode is enabled." example:"120363025000000000@g.us"`
	AllowedScopes     []string                `json:"allowedScopes,omitempty" doc:"OAuth/OIDC scopes this app may request. Defaults to openid and profile." example:"[\"openid\",\"profile\",\"phone\"]"`
	TokenTTLSeconds   int                     `json:"tokenTtlSeconds,omitempty" doc:"Access-token and id-token lifetime in seconds. Defaults to 900." example:"900"`
	RefreshTTLSeconds int                     `json:"refreshTtlSeconds,omitempty" doc:"Refresh-token family maximum lifetime in seconds. Defaults to 2592000." example:"2592000"`
}

type oauthAppPatchBody struct {
	SessionID         *string                  `json:"sessionId,omitempty" doc:"New WhatsApp session id. Must belong to the owning organization." example:"sess_01J9ZX8K2QHV0M3T6R7P4N5W8C"`
	Name              *string                  `json:"name,omitempty" doc:"Application name shown on the consent page and in WhatsApp bot replies." example:"Acme Portal"`
	LogoURL           *string                  `json:"logoUrl,omitempty" doc:"Optional HTTPS logo URL shown on the consent page." example:"https://acme.example/logo.png"`
	ClientType        *string                  `json:"clientType,omitempty" enum:"confidential,public" doc:"OAuth client type. Confidential clients may be downgraded to public, which removes the secret." example:"public"`
	LoginCommand      *string                  `json:"loginCommand,omitempty" doc:"Single-word command users type in WhatsApp before the six-digit code. Must match [a-z0-9_-]{2,32} and must not equal WHATSAPP_ADMIN_CMD_PREFIX." example:"masuk"`
	RedirectURIs      *[]string                `json:"redirectUris,omitempty" doc:"Replacement exact redirect URI set." example:"[\"https://app.example.com/oauth/callback\"]"`
	Modes             *[]apitypes.OAuthAppMode `json:"modes,omitempty" enum:"dm,group" doc:"Replacement verification modes. If group is included, groupJid is required." example:"[\"dm\",\"group\"]"`
	GroupJID          *string                  `json:"groupJid,omitempty" doc:"Pinned WhatsApp group JID. Required iff group mode is enabled." example:"120363025000000000@g.us"`
	AllowedScopes     *[]string                `json:"allowedScopes,omitempty" doc:"Replacement OAuth/OIDC scopes this app may request." example:"[\"openid\",\"profile\",\"phone\"]"`
	TokenTTLSeconds   *int                     `json:"tokenTtlSeconds,omitempty" doc:"Access-token and id-token lifetime in seconds." example:"900"`
	RefreshTTLSeconds *int                     `json:"refreshTtlSeconds,omitempty" doc:"Refresh-token family maximum lifetime in seconds." example:"2592000"`
}

func (b oauthAppBody) toInput(userID *string) service.OAuthAppCreateInput {
	modes := make([]string, 0, len(b.Modes))
	for _, m := range b.Modes {
		modes = append(modes, string(m))
	}
	return service.OAuthAppCreateInput{
		SessionID:         b.SessionID,
		Name:              b.Name,
		LogoURL:           b.LogoURL,
		ClientType:        b.ClientType,
		LoginCommand:      b.LoginCommand,
		RedirectURIs:      b.RedirectURIs,
		Modes:             modes,
		GroupJID:          b.GroupJID,
		AllowedScopes:     b.AllowedScopes,
		TokenTTLSeconds:   b.TokenTTLSeconds,
		RefreshTTLSeconds: b.RefreshTTLSeconds,
		CreatedByUserID:   userID,
	}
}

func (b oauthAppPatchBody) toInput() service.OAuthAppUpdateInput {
	var modes *[]string
	if b.Modes != nil {
		converted := make([]string, 0, len(*b.Modes))
		for _, m := range *b.Modes {
			converted = append(converted, string(m))
		}
		modes = &converted
	}
	var logo **string
	if b.LogoURL != nil {
		logo = &b.LogoURL
	}
	var group **string
	if b.GroupJID != nil {
		group = &b.GroupJID
	}
	return service.OAuthAppUpdateInput{
		SessionID:         b.SessionID,
		Name:              b.Name,
		LogoURL:           logo,
		ClientType:        b.ClientType,
		LoginCommand:      b.LoginCommand,
		RedirectURIs:      b.RedirectURIs,
		Modes:             modes,
		GroupJID:          group,
		AllowedScopes:     b.AllowedScopes,
		TokenTTLSeconds:   b.TokenTTLSeconds,
		RefreshTTLSeconds: b.RefreshTTLSeconds,
	}
}

type createOAuthAppInput struct{ Body oauthAppBody }
type updateOAuthAppInput struct {
	ID   string `path:"id" doc:"OAuth application id. Unknown or cross-organization ids return not_found unless the caller is super_admin." example:"oac_01J9ZX8K2QHV0M3T6R7P4N5W8C"`
	Body oauthAppPatchBody
}
type oauthAppIDInput struct {
	ID string `path:"id" doc:"OAuth application id. Unknown or cross-organization ids return not_found unless the caller is super_admin." example:"oac_01J9ZX8K2QHV0M3T6R7P4N5W8C"`
}
type oauthGrantIDInput struct {
	ID      string `path:"id" doc:"OAuth application id." example:"oac_01J9ZX8K2QHV0M3T6R7P4N5W8C"`
	GrantID string `path:"grantId" doc:"OAuth grant id owned by the application." example:"ogr_01J9ZX8K2QHV0M3T6R7P4N5W8C"`
}
type oauthListInput struct {
	Cursor string `query:"cursor" doc:"Opaque cursor returned by the previous page." example:"oac_01J9ZX8K2QHV0M3T6R7P4N5W8C"`
	Limit  int    `query:"limit" doc:"Maximum number of items to return. Defaults to 50 and is capped at 200." example:"50"`
}
type oauthGrantListInput struct {
	ID     string `path:"id" doc:"OAuth application id." example:"oac_01J9ZX8K2QHV0M3T6R7P4N5W8C"`
	Cursor string `query:"cursor" doc:"Opaque cursor returned by the previous page." example:"ogr_01J9ZX8K2QHV0M3T6R7P4N5W8C"`
	Limit  int    `query:"limit" doc:"Maximum number of grants to return. Defaults to 50 and is capped at 200." example:"50"`
}

type oauthAppOutput struct{ Body apitypes.OAuthApp }
type oauthAppSecretOutput struct{ Body apitypes.OAuthAppWithSecret }
type oauthAppListOutput struct {
	Body apitypes.List[apitypes.OAuthApp]
}
type oauthGrantListOutput struct {
	Body apitypes.List[apitypes.OAuthGrant]
}

func RegisterOAuthAppOps(api huma.API, h *Handlers) {
	read := huma.Middlewares{humax.RequireCap(api, authz.CapRead)}
	manage := huma.Middlewares{humax.RequireCap(api, authz.CapManage)}

	huma.Register(api, huma.Operation{
		OperationID: "listOAuthApps", Method: "GET", Path: "/api/v1/oauth-apps",
		Summary:     "List OAuth applications",
		Description: "List Sign in with WhatsApp OAuth applications owned by the caller's organization.\n\nRequires `read` capability. Results are org-scoped.",
		Tags:        []string{"OAuth Apps"}, Middlewares: read,
	}, func(ctx context.Context, in *oauthListInput) (*oauthAppListOutput, error) {
		p, err := humax.Principal(ctx)
		if err != nil {
			return nil, err
		}
		page, err := h.OAuthApps.List(ctx, p.OrganizationID, p.IsSuperAdmin(), in.Cursor, in.Limit)
		if err != nil {
			return nil, humax.Err(err)
		}
		return &oauthAppListOutput{Body: apitypes.NewList(page.Items, page.NextCursor)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "createOAuthApp", Method: "POST", Path: "/api/v1/oauth-apps",
		Summary:     "Create an OAuth application",
		Description: "Create an org-owned Sign in with WhatsApp OAuth application bound to one WhatsApp session.\n\nRequires `manage` capability. Confidential clients return `clientSecret` exactly once in this response; public clients return no secret.",
		Tags:        []string{"OAuth Apps"}, DefaultStatus: 201, Middlewares: manage,
	}, func(ctx context.Context, in *createOAuthAppInput) (*oauthAppSecretOutput, error) {
		p, err := humax.Principal(ctx)
		if err != nil {
			return nil, err
		}
		app, err := h.OAuthApps.Create(ctx, p.OrganizationID, in.Body.toInput(&p.UserID))
		if err != nil {
			return nil, humax.Err(err)
		}
		return &oauthAppSecretOutput{Body: app}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "getOAuthApp", Method: "GET", Path: "/api/v1/oauth-apps/{id}",
		Summary:     "Get an OAuth application",
		Description: "Fetch one OAuth application. Cross-organization ids return `not_found` unless the caller is `super_admin`.\n\nRequires `read` capability.",
		Tags:        []string{"OAuth Apps"}, Middlewares: read,
	}, func(ctx context.Context, in *oauthAppIDInput) (*oauthAppOutput, error) {
		p, err := humax.Principal(ctx)
		if err != nil {
			return nil, err
		}
		app, err := h.OAuthApps.Get(ctx, p.OrganizationID, in.ID, p.IsSuperAdmin())
		if err != nil {
			return nil, humax.Err(err)
		}
		return &oauthAppOutput{Body: app}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "updateOAuthApp", Method: "PATCH", Path: "/api/v1/oauth-apps/{id}",
		Summary:     "Update an OAuth application",
		Description: "Patch OAuth application configuration. Redirect URIs and modes are replacement sets; redirect URIs must remain exact absolute HTTPS URLs, with localhost HTTP allowed for development.\n\nRequires `manage` capability.",
		Tags:        []string{"OAuth Apps"}, Middlewares: manage,
	}, func(ctx context.Context, in *updateOAuthAppInput) (*oauthAppOutput, error) {
		p, err := humax.Principal(ctx)
		if err != nil {
			return nil, err
		}
		app, err := h.OAuthApps.Update(ctx, p.OrganizationID, in.ID, p.IsSuperAdmin(), in.Body.toInput())
		if err != nil {
			return nil, humax.Err(err)
		}
		return &oauthAppOutput{Body: app}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "rotateOAuthAppSecret", Method: "POST", Path: "/api/v1/oauth-apps/{id}:rotate-secret",
		Summary:     "Rotate an OAuth client secret",
		Description: "Rotate a confidential OAuth application's client_secret. The old secret is invalid immediately, and the new plaintext `clientSecret` is returned exactly once.\n\nRequires `manage` capability.",
		Tags:        []string{"OAuth Apps"}, Middlewares: manage,
	}, func(ctx context.Context, in *oauthAppIDInput) (*oauthAppSecretOutput, error) {
		p, err := humax.Principal(ctx)
		if err != nil {
			return nil, err
		}
		app, err := h.OAuthApps.RotateSecret(ctx, p.OrganizationID, in.ID, p.IsSuperAdmin())
		if err != nil {
			return nil, humax.Err(err)
		}
		return &oauthAppSecretOutput{Body: app}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "enableOAuthApp", Method: "POST", Path: "/api/v1/oauth-apps/{id}:enable",
		Summary:     "Enable an OAuth application",
		Description: "Enable an OAuth application so new authorizations and token grants are accepted.\n\nRequires `manage` capability.",
		Tags:        []string{"OAuth Apps"}, Middlewares: manage,
	}, func(ctx context.Context, in *oauthAppIDInput) (*oauthAppOutput, error) {
		p, err := humax.Principal(ctx)
		if err != nil {
			return nil, err
		}
		app, err := h.OAuthApps.SetEnabled(ctx, p.OrganizationID, in.ID, p.IsSuperAdmin(), true)
		if err != nil {
			return nil, humax.Err(err)
		}
		return &oauthAppOutput{Body: app}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "disableOAuthApp", Method: "POST", Path: "/api/v1/oauth-apps/{id}:disable",
		Summary:     "Disable an OAuth application",
		Description: "Disable an OAuth application. Existing grants are retained, but new authorizations and token grants are refused by the OAuth protocol endpoints.\n\nRequires `manage` capability.",
		Tags:        []string{"OAuth Apps"}, Middlewares: manage,
	}, func(ctx context.Context, in *oauthAppIDInput) (*oauthAppOutput, error) {
		p, err := humax.Principal(ctx)
		if err != nil {
			return nil, err
		}
		app, err := h.OAuthApps.SetEnabled(ctx, p.OrganizationID, in.ID, p.IsSuperAdmin(), false)
		if err != nil {
			return nil, humax.Err(err)
		}
		return &oauthAppOutput{Body: app}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "deleteOAuthApp", Method: "DELETE", Path: "/api/v1/oauth-apps/{id}",
		Summary:     "Delete an OAuth application",
		Description: "Soft-delete an OAuth application and cascade-revoke its grants and refresh tokens immediately.\n\nRequires `manage` capability. Returns 204 on success.",
		Tags:        []string{"OAuth Apps"}, DefaultStatus: 204, Middlewares: manage,
	}, func(ctx context.Context, in *oauthAppIDInput) (*emptyOutput, error) {
		p, err := humax.Principal(ctx)
		if err != nil {
			return nil, err
		}
		if err := h.OAuthApps.Delete(ctx, p.OrganizationID, in.ID, p.IsSuperAdmin()); err != nil {
			return nil, humax.Err(err)
		}
		return &emptyOutput{}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "listOAuthAppGrants", Method: "GET", Path: "/api/v1/oauth-apps/{id}/grants",
		Summary:     "List OAuth application grants",
		Description: "List persistent WhatsApp identity grants for one OAuth application.\n\nRequires `read` capability.",
		Tags:        []string{"OAuth Apps"}, Middlewares: read,
	}, func(ctx context.Context, in *oauthGrantListInput) (*oauthGrantListOutput, error) {
		p, err := humax.Principal(ctx)
		if err != nil {
			return nil, err
		}
		page, err := h.OAuthApps.ListGrants(ctx, p.OrganizationID, in.ID, p.IsSuperAdmin(), in.Cursor, in.Limit)
		if err != nil {
			return nil, humax.Err(err)
		}
		return &oauthGrantListOutput{Body: apitypes.NewList(page.Items, page.NextCursor)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "revokeOAuthAppGrant", Method: "DELETE", Path: "/api/v1/oauth-apps/{id}/grants/{grantId}",
		Summary:     "Revoke an OAuth application grant",
		Description: "Revoke one persistent grant and all refresh tokens issued under it. Access JWTs expire naturally by their short TTL.\n\nRequires `manage` capability. Returns 204 on success.",
		Tags:        []string{"OAuth Apps"}, DefaultStatus: 204, Middlewares: manage,
	}, func(ctx context.Context, in *oauthGrantIDInput) (*emptyOutput, error) {
		p, err := humax.Principal(ctx)
		if err != nil {
			return nil, err
		}
		if err := h.OAuthApps.RevokeGrant(ctx, p.OrganizationID, in.ID, in.GrantID, p.IsSuperAdmin()); err != nil {
			return nil, humax.Err(err)
		}
		return &emptyOutput{}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "revokeAllOAuthAppGrants", Method: "POST", Path: "/api/v1/oauth-apps/{id}/grants:revoke-all",
		Summary:     "Revoke all OAuth application grants",
		Description: "Revoke all persistent grants for one OAuth application and all refresh tokens issued under them. Access JWTs expire naturally by their short TTL.\n\nRequires `manage` capability. Returns 204 on success.",
		Tags:        []string{"OAuth Apps"}, DefaultStatus: 204, Middlewares: manage,
	}, func(ctx context.Context, in *oauthAppIDInput) (*emptyOutput, error) {
		p, err := humax.Principal(ctx)
		if err != nil {
			return nil, err
		}
		if err := h.OAuthApps.RevokeAllGrants(ctx, p.OrganizationID, in.ID, p.IsSuperAdmin()); err != nil {
			return nil, humax.Err(err)
		}
		return &emptyOutput{}, nil
	})
}
