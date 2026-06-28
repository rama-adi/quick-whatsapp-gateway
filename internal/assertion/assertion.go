// Package assertion implements the internal trust seam between the central router
// and the gateways (docs/plans/plan-router-impl.md D3). Authentication terminates
// at the router; the router resolves the full caller Principal and then, per
// proxied request, mints a short-lived Ed25519-signed JWS — the "internal
// assertion" — that the gateway verifies before acting.
//
// The assertion is request-bound and single-use so a leaked one cannot be replayed:
//
//   - aud = the target gateway id            (cannot be replayed at another gateway)
//   - session                                (bound to the session being acted on)
//   - method + path                          (cannot be replayed at a different route)
//   - bodyHash = base64url(SHA-256(body))    (cannot be reused with a swapped payload)
//   - iat/exp (~30s window) + jti nonce      (tight freshness + one-time use)
//
// The router holds the Ed25519 PRIVATE key and publishes the PUBLIC key as a JWKS;
// the gateway holds only the public key (fetched from ROUTER_JWKS_URL), so a
// compromised gateway can never forge an assertion. This mirrors how the gateway
// previously verified better-auth JWTs — same jwx machinery, repointed at the
// router's JWKS — which is why the package depends only on jwx + internal/domain
// (+ internal/authz for the shared Principal type on the gateway middleware).
package assertion

import (
	"strings"
	"time"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// Default freshness window and clock skew for assertions. The window is short
// because an assertion is minted immediately before the proxied request is sent.
const (
	DefaultTTL  = 30 * time.Second
	DefaultSkew = 5 * time.Second
)

// Claim names carried in the assertion JWS payload (in addition to the standard
// iss/aud/iat/exp/jti). Kept short to bound token size on the hot path.
const (
	claimOrg     = "org"  // resolved organization id (load-bearing isolation key)
	claimKind    = "knd"  // principal kind: "user" | "apikey"
	claimUserID  = "uid"  // better-auth user id (JWT callers)
	claimOrgRole = "orol" // org role (owner/admin/member)
	claimRole    = "prol" // platform role (e.g. super_admin)
	claimKeyID   = "kid"  // better-auth api-key id (api-key callers)
	claimPerms   = "perm" // granted capabilities as a string list
	claimSession = "sess" // the session id the request targets ("" if none)
	claimMethod  = "mth"  // HTTP method, request-bound
	claimPath    = "pth"  // request target (path[?query]), request-bound
	claimBody    = "bsh"  // base64url(SHA-256(body)), request-bound
)

// Principal is the resolved caller the router asserts to the gateway. It is the
// subset of authz.Principal that crosses the wire; the gateway middleware rebuilds
// an authz.Principal from it. Keeping a local struct (rather than importing authz
// here) avoids an import cycle: authz must never import assertion.
type Principal struct {
	Kind           string // "user" | "apikey"
	OrganizationID string
	UserID         string
	OrgRole        string
	PlatformRole   string
	KeyID          string
	Permissions    domain.Permissions
}

// Request is the per-request binding the router signs into the assertion and the
// gateway re-checks against the actual proxied request.
type Request struct {
	Gateway string // target gateway id (becomes aud)
	Session string // session id the request acts on ("" for non-session routes)
	Method  string // HTTP method
	Path    string // request target: path, plus ?query when present
	Body    []byte // raw request body (hashed; never stored)
}

// permsToString / permsFromString serialize domain.Permissions as a compact
// comma-separated string. A scalar string claim round-trips cleanly through jwx's
// typed Get (a []string claim decodes to []any and won't convert), so the wire
// form is "read,send" rather than a JSON array.
func permsToString(p domain.Permissions) string {
	out := make([]string, 0, 4)
	if p.Read {
		out = append(out, "read")
	}
	if p.Send {
		out = append(out, "send")
	}
	if p.Manage {
		out = append(out, "manage")
	}
	if p.Events {
		out = append(out, "events")
	}
	return strings.Join(out, ",")
}

func permsFromString(s string) domain.Permissions {
	var p domain.Permissions
	for _, c := range strings.Split(s, ",") {
		switch c {
		case "read":
			p.Read = true
		case "send":
			p.Send = true
		case "manage":
			p.Manage = true
		case "events":
			p.Events = true
		}
	}
	return p
}
