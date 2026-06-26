package authz

import (
	"net/http"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/httpx"
)

// Capability is one of the four gated actions (§4.3). For api-key callers it maps
// to the key's permission flag; for JWT callers it maps to an org-role policy.
type Capability string

const (
	CapRead   Capability = "read"
	CapSend   Capability = "send"
	CapManage Capability = "manage"
	CapEvents Capability = "events"
)

// Allow reports whether p may perform cap (§4.3):
//
//   - super_admin (platform) is allowed everything, across orgs.
//   - api-key callers: the matching KeyPermissions flag must be set.
//   - JWT callers (org role): owner/admin may do anything within the org
//     (read/send/manage/events); member may read/send/events but not manage.
//
// A nil principal is denied.
func Allow(p *Principal, cap Capability) bool {
	if p == nil {
		return false
	}
	if p.IsSuperAdmin() {
		return true
	}
	switch p.Kind {
	case KindAPIKey:
		return keyPermits(p.KeyPermissions, cap)
	case KindUser:
		return orgRolePermits(p.OrgRole, cap)
	default:
		return false
	}
}

func keyPermits(perms domain.Permissions, cap Capability) bool {
	switch cap {
	case CapRead:
		return perms.Read
	case CapSend:
		return perms.Send
	case CapManage:
		return perms.Manage
	case CapEvents:
		return perms.Events
	default:
		return false
	}
}

func orgRolePermits(role string, cap Capability) bool {
	switch role {
	case OrgRoleOwner, OrgRoleAdmin:
		// owner/admin manage the org → all capabilities.
		return true
	case OrgRoleMember:
		// member: read/send/events, but not manage (§4.3).
		return cap != CapManage
	default:
		return false
	}
}

// Require returns middleware that 403s unless the context Principal is allowed
// the capability. It 401s when no principal is present (Authenticate must run
// first). It is the unified replacement for the api-key-only RequireRead/Send/…
// gates in the old middleware package.
func Require(cap Capability) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := FromContext(r.Context())
			if p == nil {
				httpx.WriteError(w, domain.ErrUnauthorized("authentication required"))
				return
			}
			if !Allow(p, cap) {
				httpx.WriteError(w, domain.ErrForbidden("missing required capability: "+string(cap)))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireRead gates a route on the read capability.
func RequireRead() func(http.Handler) http.Handler { return Require(CapRead) }

// RequireSend gates a route on the send capability.
func RequireSend() func(http.Handler) http.Handler { return Require(CapSend) }

// RequireManage gates a route on the manage capability.
func RequireManage() func(http.Handler) http.Handler { return Require(CapManage) }

// RequireEvents gates a route on the events capability (the NDJSON stream).
func RequireEvents() func(http.Handler) http.Handler { return Require(CapEvents) }

// RequireSuperAdmin gates a route on the platform super_admin role (cross-org
// oversight, §4.3) — e.g. GET /admin/sessions and GET /contacts/{lid}.
func RequireSuperAdmin() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := FromContext(r.Context())
			if p == nil {
				httpx.WriteError(w, domain.ErrUnauthorized("authentication required"))
				return
			}
			if !p.IsSuperAdmin() {
				httpx.WriteError(w, domain.ErrForbidden("super_admin required"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
