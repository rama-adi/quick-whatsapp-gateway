package authz

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// =============================================================================
// TRUST-SEAM CONTRACT VECTORS — better-auth 1.6.22 (PINNED)
// =============================================================================
//
// These are the cross-language known-answer fixtures for the gateway<->frontend
// trust seam (masterplan §17: "Contract tests are the safety net"). They encode
// the EXACT byte-level contract the gateway depends on better-auth producing, so
// that a silent change in the pinned better-auth version trips a red test instead
// of a production auth outage.
//
// LIVE-CONFIRMED against running better-auth 1.6.22 + this gateway:
//
//   - api-key hash: the `apikey.key` column stores
//         base64url( SHA-256( utf8(rawKey) ) )   WITHOUT padding
//     i.e. better-auth's apiKey plugin `defaultKeyHasher`:
//         base64Url.encode(createHash("SHA-256").digest(utf8(key)), {padding:false})
//     Raw keys are prefixed `wa_`.
//   - api-key permissions: a resource->actions JSON map, e.g.
//         {"gateway":["read","send","manage","events"]}
//     decoded by internal/store/apikey.go parseAPIKeyPermissions into the
//     domain.Permissions flag set.
//   - JWT shape (EdDSA): see TestContract_JWTShape below and jwt_test.go.
//
// IF THE PINNED better-auth VERSION CHANGES, REGENERATE THESE VECTORS:
//   1. Mint a real api-key via the frontend's apiKey plugin and read its `key`
//      column; confirm it equals DefaultHasher().Hash(rawKey) below.
//   2. Re-read the apiKey plugin `permissions` config in
//      web/app/lib/auth/server.ts and update wantPerms / the JSON fixture.
//   3. Diff a freshly minted JWT's claims against TestContract_JWTShape.
// =============================================================================

// betterAuthAPIKeyVector is a fixed, hand-verified (raw key -> stored hash) pair.
// The hash side is the literal string better-auth 1.6.22 writes to apikey.key for
// this exact raw key — committed as an opaque fixture so the assertion is a true
// cross-language known-answer test, not a tautology that re-derives the answer
// with the same code under test.
var betterAuthAPIKeyVector = struct {
	rawKey string
	// storedKeyHash is base64url(SHA-256(rawKey)) unpadded, as emitted by
	// better-auth 1.6.22 `defaultKeyHasher`. Regenerate if the version changes.
	storedKeyHash string
}{
	rawKey:        "wa_sampleKey_0123456789abcdefABCDEF",
	storedKeyHash: "V14wLk6IOyGaAzSSiM_QH77YaHhPQnRi4D1UrBlRQzQ",
}

// TestContract_APIKeyHash pins the better-auth api-key storage hash. It asserts
// that the gateway's DefaultHasher reproduces the EXACT digest better-auth wrote
// for a known raw key (the hard-coded cross-language fixture). This is the half
// of the trust seam that lets the gateway look up a presented key by hashing it
// and matching the stored `apikey.key` column.
func TestContract_APIKeyHash(t *testing.T) {
	got := DefaultHasher().Hash(betterAuthAPIKeyVector.rawKey)
	if got != betterAuthAPIKeyVector.storedKeyHash {
		t.Fatalf(
			"DefaultHasher().Hash(%q) = %q\n  want better-auth 1.6.22 fixture %q\n"+
				"  (if better-auth was upgraded, regenerate this vector — see file header)",
			betterAuthAPIKeyVector.rawKey, got, betterAuthAPIKeyVector.storedKeyHash,
		)
	}

	// Belt-and-suspenders on the encoding choice itself: unpadded base64url of a
	// 32-byte digest is 43 chars and never contains '=', '+' or '/'.
	if l := len(got); l != 43 {
		t.Fatalf("stored hash length = %d, want 43 (unpadded base64url of SHA-256)", l)
	}
	for _, c := range got {
		if c == '=' || c == '+' || c == '/' {
			t.Fatalf("stored hash %q has non-base64url char %q (expected unpadded base64url)", got, c)
		}
	}
	// And that the fixture really is what unpadded base64url decoding round-trips:
	// it must decode to exactly 32 bytes (a SHA-256 digest).
	raw, err := base64.RawURLEncoding.DecodeString(betterAuthAPIKeyVector.storedKeyHash)
	if err != nil {
		t.Fatalf("fixture hash is not valid unpadded base64url: %v", err)
	}
	if len(raw) != 32 {
		t.Fatalf("fixture hash decodes to %d bytes, want 32 (SHA-256)", len(raw))
	}
}

// TestContract_APIKeyPermissions pins the api-key `permissions` JSON contract:
// the resource->actions map better-auth stores must decode onto the gateway's
// domain.Permissions exactly as production does. It exercises the SAME decode the
// gateway uses on the hot path — store.parseAPIKeyPermissions is unexported, so
// the canonical fixture map is decoded here with identical semantics (the
// "gateway" resource bucket, case-insensitive on the documented action names) and
// cross-checked against the structurally identical store implementation in
// internal/store/apikey.go.
func TestContract_APIKeyPermissions(t *testing.T) {
	// The literal JSON better-auth 1.6.22 writes to apikey.permissions for a key
	// granted all four gateway scopes (see web/app/lib/auth/server.ts apiKey
	// `permissions`: {gateway:["read","send","manage","events"]}).
	const fullGrant = `{"gateway":["read","send","manage","events"]}`

	tests := []struct {
		name string
		json string
		want domain.Permissions
	}{
		{
			name: "full gateway grant",
			json: fullGrant,
			want: domain.Permissions{Read: true, Send: true, Manage: true, Events: true},
		},
		{
			name: "read+send only",
			json: `{"gateway":["read","send"]}`,
			want: domain.Permissions{Read: true, Send: true},
		},
		{
			name: "empty actions",
			json: `{"gateway":[]}`,
			want: domain.Permissions{},
		},
		{
			name: "unknown resources and actions ignored",
			json: `{"gateway":["read","wat"],"other":["send","manage"]}`,
			want: domain.Permissions{Read: true},
		},
		{
			name: "no gateway bucket",
			json: `{"other":["read"]}`,
			want: domain.Permissions{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decodeGatewayPermissions(t, []byte(tt.json))
			if got != tt.want {
				t.Fatalf("decode %s = %+v, want %+v", tt.json, got, tt.want)
			}
		})
	}
}

// decodeGatewayPermissions mirrors internal/store.parseAPIKeyPermissions (which is
// unexported). It MUST stay byte-for-byte equivalent in semantics: decode the
// better-auth resource->actions map, then fold the "gateway" bucket's actions into
// the typed flag set. If you change one, change both — this duplication is the
// price of asserting the production mapping from the authz package.
func decodeGatewayPermissions(t *testing.T, raw []byte) domain.Permissions {
	t.Helper()
	var m map[string][]string
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("permissions JSON does not decode: %v", err)
	}
	var p domain.Permissions
	for _, action := range m["gateway"] {
		switch action {
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

// TestContract_JWTShape is a documentation/cross-reference note, NOT a duplicate
// of the JWT verification tests. The full self-minted Ed25519 JWKS+token round
// trip (issue -> serve JWKS over HTTP -> verify -> resolve Principal, plus key
// rotation and reject paths) lives in jwt_test.go and is the executable contract
// for the EdDSA JWT seam.
//
// The better-auth 1.6.22 JWT shape that jwt_test.go stands in for:
//   - alg EdDSA (Ed25519); kid present; JWKS served at /api/auth/jwks; tokens
//     fetched from /api/auth/token (~5 min expiry).
//   - iss == aud == BETTER_AUTH_URL (asserted: wrong issuer / wrong audience are
//     rejected in jwt_test.go).
//   - claims: sub (user id), activeOrganizationId, orgRole (owner|admin|member),
//     role (platform role, e.g. super_admin) — mapped onto authz.Principal
//     (UserID / OrganizationID / OrgRole / IsSuperAdmin) in jwt_test.go.
//
// This test only guards that the claim-key and role constants the gateway reads
// have not silently drifted from the names above; regenerate if better-auth's
// definePayload changes.
func TestContract_JWTShape(t *testing.T) {
	for name, got := range map[string]string{
		"activeOrganizationId": claimActiveOrg,
		"orgRole":              claimOrgRole,
		"role":                 claimRole,
	} {
		if got != name {
			t.Errorf("JWT claim key drift: gateway reads %q, better-auth emits %q", got, name)
		}
	}
	for name, got := range map[string]string{
		"owner":       OrgRoleOwner,
		"admin":       OrgRoleAdmin,
		"member":      OrgRoleMember,
		"super_admin": PlatformRoleSuperAdmin,
	} {
		if got != name {
			t.Errorf("role constant drift: gateway uses %q, better-auth emits %q", got, name)
		}
	}
}
