package assertion

import (
	"bytes"
	"io"
	"net/http"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/authz"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	httpmiddleware "github.com/ramaadi/quick-whatsapp-gateway/internal/http/middleware"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/httpx"
)

// HeaderAssertion is the header the router attaches the internal assertion on (the
// inbound end-user Authorization is stripped first). A dedicated header keeps the
// internal token clearly separate from end-user credentials.
const HeaderAssertion = "X-Internal-Assertion"

// defaultMaxBody bounds how much request body the middleware buffers to recompute
// the body hash. It must exceed the largest legitimate request (the 256 MiB backup
// upload). Configurable via WithMaxBody.
const defaultMaxBody = 300 << 20 // 300 MiB

// Middleware verifies the router's internal assertion on every gateway request
// and populates authz.Principal, replacing the gateway's old client-facing authn.
// It buffers the bounded request body because verification must hash the exact
// bytes consumed downstream. The original body is always closed after the read;
// on success a new owned reader is installed, and on failure no downstream code
// receives a partially consumed stream.
//
// Verification detail is deliberately collapsed into a generic 401. After a
// success, capability gates and organization-scoped stores depend only on the
// asserted principal and never inspect the original caller credential.
func Middleware(v *Verifier, opts ...MiddlewareOption) func(http.Handler) http.Handler {
	cfg := middlewareConfig{maxBody: defaultMaxBody}
	for _, o := range opts {
		o(&cfg)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := r.Header.Get(HeaderAssertion)
			if raw == "" {
				httpx.WriteError(w, domain.ErrUnauthorized("missing internal assertion"))
				return
			}

			// Buffer the body so we can both hash it for the binding check and hand
			// it to the downstream handler unchanged.
			var body []byte
			if r.Body != nil {
				limited := http.MaxBytesReader(w, r.Body, cfg.maxBody)
				b, err := io.ReadAll(limited)
				_ = limited.Close()
				if err != nil {
					httpx.WriteError(w, domain.ErrValidation("request body too large or unreadable"))
					return
				}
				body = b
				r.Body = io.NopCloser(bytes.NewReader(b))
			}

			p, err := v.Verify(r.Context(), raw, Binding{
				Method: r.Method,
				Path:   RequestTarget(r),
				Body:   body,
			})
			if err != nil {
				httpx.WriteError(w, domain.ErrUnauthorized("invalid internal assertion"))
				return
			}

			ap := &authz.Principal{
				Kind:           authz.PrincipalKind(p.Kind),
				OrganizationID: p.OrganizationID,
				UserID:         p.UserID,
				OrgRole:        p.OrgRole,
				PlatformRole:   p.PlatformRole,
				KeyID:          p.KeyID,
				KeyPermissions: p.Permissions,
			}
			httpmiddleware.SetRequestOrganization(r.Context(), ap.OrganizationID)
			next.ServeHTTP(w, r.WithContext(authz.SetPrincipal(r.Context(), ap)))
		})
	}
}

type middlewareConfig struct{ maxBody int64 }

// MiddlewareOption configures the assertion middleware.
type MiddlewareOption func(*middlewareConfig)

// WithMaxBody caps how much request body is buffered for hashing (default 300 MiB).
func WithMaxBody(n int64) MiddlewareOption {
	return func(c *middlewareConfig) {
		if n > 0 {
			c.maxBody = n
		}
	}
}
