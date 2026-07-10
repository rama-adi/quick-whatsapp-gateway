package authz

import (
	"net/http"
	"strings"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/httpx"
)

// Authenticate is the single auth middleware with two acceptors evaluated in
// order (§4.3):
//
//  1. An Authorization: Bearer credential that PARSES AS A JWT is verified via
//     JWKS → a human Principal {UserID, OrganizationID(active), OrgRole,
//     PlatformRole}.
//  2. Otherwise the bearer / x-api-key credential is treated as an api-key and
//     verified against the shared `apikey` table → an org-scoped api-key
//     Principal {OrganizationID, KeyPermissions} (no UserID).
//  3. Neither → 401.
//
// On success the Principal (and its organization id) are placed on the request
// context (SetPrincipal). The middleware never leaks why verification failed.
func Authenticate(tokens TokenVerifier, keys KeyVerifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			bearer, hasBearer := bearerToken(r)

			// Acceptor 1: a bearer that looks like a JWT → JWKS verify.
			if hasBearer && looksLikeJWT(bearer) {
				if tokens != nil {
					if p, err := tokens.VerifyToken(r.Context(), bearer); err == nil && validPrincipal(p, KindUser) {
						next.ServeHTTP(w, r.WithContext(SetPrincipal(r.Context(), p)))
						return
					}
				}
				// Never reinterpret a failed JWT itself as an API key. A separately
				// supplied X-Api-Key may still use the second acceptor.
			}

			// Acceptor 2: an explicit X-Api-Key takes precedence over an opaque
			// bearer when both are present.
			raw := r.Header.Get("X-Api-Key")
			if raw == "" && hasBearer && !looksLikeJWT(bearer) {
				raw = bearer
			}
			if raw != "" && keys != nil {
				if p, err := keys.VerifyKey(r.Context(), raw); err == nil && validPrincipal(p, KindAPIKey) {
					next.ServeHTTP(w, r.WithContext(SetPrincipal(r.Context(), p)))
					return
				}
			}

			httpx.WriteError(w, domain.ErrUnauthorized("missing or invalid credentials"))
		})
	}
}

// validPrincipal is the final structural check between credential verification
// and authorization context. Human identities must carry a user ID; API keys must
// carry both their owning organization and stable key ID. A nil, cross-kind, or
// partial result from a buggy verifier is treated exactly like invalid credentials
// and never reaches a handler.
func validPrincipal(p *Principal, want PrincipalKind) bool {
	if p == nil || p.Kind != want {
		return false
	}
	switch want {
	case KindUser:
		return p.UserID != ""
	case KindAPIKey:
		return p.OrganizationID != "" && p.KeyID != ""
	default:
		return false
	}
}

// bearerToken extracts the credential from "Authorization: Bearer <token>"
// (scheme match is case-insensitive). The bool reports success.
func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const prefix = "bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	tok := strings.TrimSpace(h[len(prefix):])
	if tok == "" {
		return "", false
	}
	return tok, true
}

// looksLikeJWT reports whether tok has the three-segment compact-JWS shape
// (header.payload.signature) — a cheap structural check to route between the two
// acceptors. better-auth api-keys are not dotted base64 triples, so this
// reliably separates the two. The verifier still does the real validation.
func looksLikeJWT(tok string) bool {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
	}
	return true
}
