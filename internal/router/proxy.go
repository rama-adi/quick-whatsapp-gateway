package router

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/assertion"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/authz"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/httpx"
)

// maxProxyBody caps how much request body the router buffers to bind it into the
// assertion (must exceed the 256 MiB backup upload). The owning gateway applies
// its own MaxBytesReader downstream.
const maxProxyBody = 300 << 20

// broker is the heart of the router: it mints a request-bound internal assertion
// for the resolved principal and reverse-proxies the request to the owning
// gateway. The inbound end-user credential is stripped; the gateway trusts only
// the assertion.
//
// session is "" for gateway-agnostic / placement routes (the assertion still binds
// method/path/body/aud, just not a specific session).
func (s *Server) broker(w http.ResponseWriter, r *http.Request, gateway domain.Gateway, session string) {
	p := authz.FromContext(r.Context())
	if p == nil { // Authenticate middleware guarantees this; defensive.
		httpx.WriteError(w, domain.ErrUnauthorized("authentication required"))
		return
	}
	if gateway.BaseURL == nil || *gateway.BaseURL == "" {
		httpx.WriteError(w, domain.ErrUnavailable("owning gateway has no base url registered"))
		return
	}
	target, err := url.Parse(*gateway.BaseURL)
	if err != nil || target.Scheme == "" || target.Host == "" {
		httpx.WriteError(w, domain.ErrUnavailable("owning gateway base url is invalid"))
		return
	}

	// Buffer the body so we can hash it into the assertion and still forward it.
	body, err := bufferBody(w, r)
	if err != nil {
		httpx.WriteError(w, domain.ErrValidation("request body too large or unreadable"))
		return
	}

	tok, err := s.minter.Mint(toAssertionPrincipal(p), assertion.Request{
		Gateway: gateway.ID,
		Session: session,
		Method:  r.Method,
		Path:    assertion.RequestTarget(r),
		Body:    body,
	})
	if err != nil {
		s.log.Error("mint assertion failed", "gateway", gateway.ID, "err", err)
		httpx.WriteError(w, domain.ErrInternal("failed to authorize upstream request"))
		return
	}

	s.reverseProxy(target, tok).ServeHTTP(w, r)
}

// bufferBody reads (and resets) the request body so it can be both hashed and
// forwarded. ContentLength is fixed up so a previously-chunked upload forwards
// with a known length.
func bufferBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	limited := http.MaxBytesReader(w, r.Body, maxProxyBody)
	b, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	r.Body = io.NopCloser(bytes.NewReader(b))
	r.ContentLength = int64(len(b))
	return b, nil
}

// reverseProxy builds a ReverseProxy to one gateway that rewrites the request to
// the target host, strips the end-user credential, and attaches the internal
// assertion. A 5xx from the upstream surfaces as a clean 503 envelope.
func (s *Server) reverseProxy(target *url.URL, assertionToken string) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Transport: s.transport,
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host
			// The gateway trusts only the assertion; never forward end-user creds.
			req.Header.Del("Authorization")
			req.Header.Del("X-Api-Key")
			req.Header.Set(assertion.HeaderAssertion, assertionToken)
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			s.log.Warn("upstream gateway proxy error", "target", target.String(), "err", err)
			httpx.WriteError(w, domain.ErrUnavailable("owning gateway did not respond"))
		},
	}
}

// toAssertionPrincipal projects the resolved authz.Principal onto the wire subset
// the assertion carries.
func toAssertionPrincipal(p *authz.Principal) assertion.Principal {
	return assertion.Principal{
		Kind:           string(p.Kind),
		OrganizationID: p.OrganizationID,
		UserID:         p.UserID,
		OrgRole:        p.OrgRole,
		PlatformRole:   p.PlatformRole,
		KeyID:          p.KeyID,
		Permissions:    p.KeyPermissions,
	}
}
