package authz

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// fakeTokenVerifier / fakeKeyVerifier are table-driven stand-ins for the real
// verifiers so the middleware's routing logic can be tested in isolation.
type fakeTokenVerifier struct {
	p   *Principal
	err error
}

func (f fakeTokenVerifier) VerifyToken(_ context.Context, _ string) (*Principal, error) {
	return f.p, f.err
}

type fakeKeyVerifier struct {
	p   *Principal
	err error
}

func (f fakeKeyVerifier) VerifyKey(_ context.Context, _ string) (*Principal, error) {
	return f.p, f.err
}

func captureHandler(captured **Principal) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*captured = FromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
}

// a structurally-valid (unverified) JWT-shaped string: three non-empty segments.
const jwtShaped = "aaa.bbb.ccc"

func TestAuthenticate(t *testing.T) {
	userP := &Principal{Kind: KindUser, UserID: "u1", OrganizationID: "org_u"}
	keyP := &Principal{Kind: KindAPIKey, OrganizationID: "org_k", KeyID: "k1"}

	tests := []struct {
		name       string
		authHeader string
		apiKeyHdr  string
		tokens     TokenVerifier
		keys       KeyVerifier
		wantStatus int
		wantKind   PrincipalKind
		wantOrg    string
	}{
		{
			name:       "jwt-shaped bearer verified as JWT",
			authHeader: "Bearer " + jwtShaped,
			tokens:     fakeTokenVerifier{p: userP},
			keys:       fakeKeyVerifier{err: errors.New("should not be called")},
			wantStatus: http.StatusOK, wantKind: KindUser, wantOrg: "org_u",
		},
		{
			name:       "non-jwt bearer verified as api-key",
			authHeader: "Bearer ba_opaque_key",
			tokens:     fakeTokenVerifier{err: errors.New("not a jwt")},
			keys:       fakeKeyVerifier{p: keyP},
			wantStatus: http.StatusOK, wantKind: KindAPIKey, wantOrg: "org_k",
		},
		{
			name:       "x-api-key header verified as api-key",
			apiKeyHdr:  "ba_opaque_key",
			tokens:     fakeTokenVerifier{err: errors.New("nope")},
			keys:       fakeKeyVerifier{p: keyP},
			wantStatus: http.StatusOK, wantKind: KindAPIKey, wantOrg: "org_k",
		},
		{
			name:       "jwt-shaped bearer that fails JWT verify is rejected (not retried as key)",
			authHeader: "Bearer " + jwtShaped,
			tokens:     fakeTokenVerifier{err: errors.New("bad sig")},
			keys:       fakeKeyVerifier{p: keyP},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "no credentials rejected",
			tokens:     fakeTokenVerifier{p: userP},
			keys:       fakeKeyVerifier{p: keyP},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "invalid api-key rejected",
			authHeader: "Bearer ba_opaque_key",
			tokens:     fakeTokenVerifier{},
			keys:       fakeKeyVerifier{err: errors.New("revoked")},
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var captured *Principal
			h := Authenticate(tt.tokens, tt.keys)(captureHandler(&captured))
			r := httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil)
			if tt.authHeader != "" {
				r.Header.Set("Authorization", tt.authHeader)
			}
			if tt.apiKeyHdr != "" {
				r.Header.Set("X-Api-Key", tt.apiKeyHdr)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantStatus == http.StatusOK {
				if captured == nil {
					t.Fatal("expected principal on context")
				}
				if captured.Kind != tt.wantKind {
					t.Errorf("kind = %v, want %v", captured.Kind, tt.wantKind)
				}
				if captured.OrganizationID != tt.wantOrg {
					t.Errorf("org = %q, want %q", captured.OrganizationID, tt.wantOrg)
				}
			}
		})
	}
}

func TestLooksLikeJWT(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"aaa.bbb.ccc", true},
		{"ba_opaque_key", false},
		{"only.two", false},
		{"a.b.c.d", false},
		{"aaa..ccc", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := looksLikeJWT(tt.in); got != tt.want {
			t.Errorf("looksLikeJWT(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func decodeErrCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var b domain.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if b.Error == nil {
		return ""
	}
	return b.Error.Code
}
