package assertion

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
)

// BodyHash is the canonical body binding: unpadded base64url of SHA-256 over the
// raw request body (the empty hash for an empty/absent body). The router signs it;
// the gateway recomputes it over the bytes it actually received and compares.
func BodyHash(body []byte) string {
	sum := sha256.Sum256(body)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// RequestTarget is the canonical path binding: the request path plus its raw query
// when present. Both sides derive it the same way so the proxy preserving URL.Path
// and URL.RawQuery yields a byte-identical binding.
func RequestTarget(r *http.Request) string {
	t := r.URL.Path
	if r.URL.RawQuery != "" {
		t += "?" + r.URL.RawQuery
	}
	return t
}
