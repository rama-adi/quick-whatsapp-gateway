package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"
)

// setupE2EJWT exposes a real JWKS endpoint to the child API process and returns
// a signed better-auth-shaped platform administrator token.
func setupE2EJWT(t *testing.T, infra *e2eInfra) string {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	privateJWK, err := jwk.Import(private)
	if err != nil {
		t.Fatal(err)
	}
	publicJWK, err := jwk.Import(public)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []jwk.Key{privateJWK, publicJWK} {
		if err := key.Set(jwk.KeyIDKey, "e2e-admin-key"); err != nil {
			t.Fatal(err)
		}
		if err := key.Set(jwk.AlgorithmKey, jwa.EdDSA()); err != nil {
			t.Fatal(err)
		}
	}
	set := jwk.NewSet()
	if err := set.AddKey(publicJWK); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/auth/jwks" {
			http.NotFound(w, request)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(set)
	}))
	t.Cleanup(server.Close)
	infra.authURL = server.URL
	token, err := jwt.NewBuilder().
		Issuer(server.URL).
		Audience([]string{server.URL}).
		Subject("e2e-super-admin").
		IssuedAt(time.Now()).
		Expiration(time.Now().Add(time.Hour)).
		Claim("activeOrganizationId", e2eOrgA).
		Claim("orgRole", "owner").
		Claim("role", "super_admin").
		Build()
	if err != nil {
		t.Fatal(err)
	}
	signed, err := jwt.Sign(token, jwt.WithKey(jwa.EdDSA(), privateJWK))
	if err != nil {
		t.Fatal(err)
	}
	return string(signed)
}
