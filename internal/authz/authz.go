// Package authz is the gateway's trust seam (masterplan §4). The gateway has NO
// human login — it VERIFIES two caller identities and never authenticates them
// from scratch:
//
//   - Humans authenticate with a better-auth JWT, verified LOCALLY against a JWKS
//     fetched and cached from the frontend (§4.1). No per-request callback.
//   - Machines authenticate with a better-auth api-key, verified against the shared
//     MySQL `apikey` table by hashing the presented key (§4.2).
//
// Both resolve to a Principal carrying the better-auth organization id that owns
// the caller's resources, plus RBAC inputs (org role + platform role for JWTs,
// key permissions for api-keys). Handlers authorize per-resource by organization
// id; the RequireRead/Send/Manage/Events gates here turn a Principal into an
// allow/deny decision.
//
// The package defines small consumer interfaces (TokenVerifier, KeyVerifier) so
// the middleware and main depend on abstractions, not concrete clients.
package authz

import "github.com/ramaadi/quick-whatsapp-gateway/internal/domain"

// PrincipalKind distinguishes the two caller identities (§4.3).
type PrincipalKind string

const (
	// KindUser is a human caller authenticated by a better-auth JWT.
	KindUser PrincipalKind = "user"
	// KindAPIKey is a programmatic caller authenticated by a better-auth api-key.
	KindAPIKey PrincipalKind = "apikey"
)

// Org roles (better-auth organization plugin) carried in the JWT (§4.1/§4.3).
const (
	OrgRoleOwner  = "owner"
	OrgRoleAdmin  = "admin"
	OrgRoleMember = "member"
)

// PlatformRoleSuperAdmin is the better-auth admin-plugin role granted cross-org
// oversight (§4.3). It is carried in the JWT `role` claim.
const PlatformRoleSuperAdmin = "super_admin"

// Principal is the verified caller resolved by the auth middleware and placed on
// the request context. Exactly one of UserID / api-key identity is set, captured
// by Kind. OrganizationID is the active org whose resources the caller may reach.
type Principal struct {
	Kind PrincipalKind

	// OrganizationID is the better-auth organization id that owns the resources
	// this caller may act on (the JWT's activeOrganizationId, or the api-key's
	// owning org). Always set for a valid principal.
	OrganizationID string

	// --- JWT (KindUser) fields ---

	// UserID is the better-auth user id (JWT `sub`). Empty for api-key callers.
	UserID string
	// OrgRole is the caller's role within OrganizationID (owner/admin/member),
	// from the JWT. Empty for api-key callers.
	OrgRole string
	// PlatformRole is the better-auth admin-plugin role (e.g. super_admin), from
	// the JWT `role` claim. Empty/"user" for ordinary callers and api-keys.
	PlatformRole string

	// --- api-key (KindAPIKey) fields ---

	// KeyPermissions gates an api-key caller's actions {read,send,manage,events}.
	// Zero value (all false) for JWT callers — their access is role-driven.
	KeyPermissions domain.Permissions
	// KeyID is the better-auth apikey row id (audit / cache eviction). Empty for
	// JWT callers.
	KeyID string
}

// IsSuperAdmin reports whether the principal is a platform super_admin able to
// cross organizations for oversight (§4.3). Only JWT callers can be super_admin;
// api-keys are always org-scoped.
func (p *Principal) IsSuperAdmin() bool {
	return p != nil && p.Kind == KindUser && p.PlatformRole == PlatformRoleSuperAdmin
}
