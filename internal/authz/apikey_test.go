package authz

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// TestDefaultHasher documents and pins the better-auth api-key hashing
// ASSUMPTION (R5 contract test must confirm this against the pinned better-auth
// version): the stored `apikey.key` is base64url(SHA-256(rawKey)) WITHOUT
// padding. Source: better-auth v1.6.x packages/api-key/src/index.ts
// `defaultKeyHasher`: createHash("SHA-256").digest(utf8(key)) then
// base64Url.encode(..., {padding:false}).
func TestDefaultHasher(t *testing.T) {
	h := DefaultHasher()

	// Known-answer: hash of "wak_example" computed independently below.
	const raw = "ba_test_key_value"
	sum := sha256.Sum256([]byte(raw))
	want := base64.RawURLEncoding.EncodeToString(sum[:])

	got := h.Hash(raw)
	if got != want {
		t.Fatalf("Hash(%q) = %q, want %q", raw, got, want)
	}
	// Guard the encoding choice explicitly: must be unpadded base64url (no '='),
	// not hex, not standard base64 (no '+'/'/').
	for _, c := range got {
		if c == '=' || c == '+' || c == '/' {
			t.Fatalf("hash %q contains a non-base64url char %q", got, c)
		}
	}
	if len(got) != 43 { // 32 bytes → 43 base64url chars, unpadded
		t.Fatalf("hash length = %d, want 43 (unpadded base64url of 32 bytes)", len(got))
	}
}

// fakeKeyRepo is a table-driven stand-in for store.APIKeyRepo: it maps a stored
// hash to a row (or returns not_found).
type fakeKeyRepo struct {
	byHash map[string]domain.APIKey
}

func (r fakeKeyRepo) GetByHash(_ context.Context, keyHash string) (domain.APIKey, error) {
	k, ok := r.byHash[keyHash]
	if !ok {
		return domain.APIKey{}, errors.New("not found")
	}
	return k, nil
}

func ptrI64(v int64) *int64 { return &v }

func TestAPIKeyVerifier_VerifyKey(t *testing.T) {
	const raw = "ba_secret_key"
	hash := DefaultHasher().Hash(raw)
	now := time.UnixMilli(1_700_000_000_000)

	base := domain.APIKey{
		ID:             "key_1",
		KeyHash:        hash,
		OrganizationID: "org_abc",
		Enabled:        true,
		Permissions:    domain.Permissions{Read: true, Send: true},
	}

	tests := []struct {
		name    string
		raw     string
		row     domain.APIKey
		present bool
		wantErr bool
		check   func(t *testing.T, p *Principal)
	}{
		{
			name: "valid enabled key", raw: raw, row: base, present: true,
			check: func(t *testing.T, p *Principal) {
				if p.Kind != KindAPIKey {
					t.Errorf("kind = %v, want apikey", p.Kind)
				}
				if p.OrganizationID != "org_abc" {
					t.Errorf("org = %q, want org_abc", p.OrganizationID)
				}
				if p.KeyID != "key_1" {
					t.Errorf("keyID = %q, want key_1", p.KeyID)
				}
				if p.UserID != "" {
					t.Errorf("UserID = %q, want empty (api-key has no user)", p.UserID)
				}
				if !p.KeyPermissions.Read || !p.KeyPermissions.Send {
					t.Errorf("perms = %+v, want read+send", p.KeyPermissions)
				}
			},
		},
		{
			name: "unknown key rejected", raw: raw, present: false, wantErr: true,
		},
		{
			name: "empty key rejected", raw: "", present: false, wantErr: true,
		},
		{
			name: "disabled key rejected", raw: raw, present: true, wantErr: true,
			row: func() domain.APIKey { k := base; k.Enabled = false; return k }(),
		},
		{
			name: "expired key rejected", raw: raw, present: true, wantErr: true,
			row: func() domain.APIKey { k := base; k.ExpiresAt = ptrI64(now.UnixMilli() - 1); return k }(),
		},
		{
			name: "future expiry accepted", raw: raw, present: true,
			row:   func() domain.APIKey { k := base; k.ExpiresAt = ptrI64(now.UnixMilli() + 60_000); return k }(),
			check: func(t *testing.T, p *Principal) {},
		},
		{
			name: "key without org rejected", raw: raw, present: true, wantErr: true,
			row: func() domain.APIKey { k := base; k.OrganizationID = ""; return k }(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := fakeKeyRepo{byHash: map[string]domain.APIKey{}}
			if tt.present {
				repo.byHash[hash] = tt.row
			}
			v, err := NewAPIKeyVerifier(repo, nil)
			if err != nil {
				t.Fatalf("new verifier: %v", err)
			}
			v.now = func() time.Time { return now }

			p, err := v.VerifyKey(context.Background(), tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", p)
				}
				return
			}
			if err != nil {
				t.Fatalf("VerifyKey: %v", err)
			}
			if tt.check != nil {
				tt.check(t, p)
			}
		})
	}
}

func TestNewAPIKeyVerifier_NilRepo(t *testing.T) {
	if _, err := NewAPIKeyVerifier(nil, nil); err == nil {
		t.Fatal("expected error for nil repo")
	}
}
