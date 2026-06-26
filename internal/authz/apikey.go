package authz

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// KeyVerifier verifies a raw api-key and resolves it to a Principal. It is the
// consumer-defined abstraction the auth middleware depends on (§4.3); the
// concrete implementation is *APIKeyVerifier (local hash + DB lookup), with
// *RemoteKeyVerifier as a documented fallback.
type KeyVerifier interface {
	// VerifyKey resolves raw to an org-scoped api-key Principal, or an error that
	// callers map to 401. The principal never carries a UserID (§4.2).
	VerifyKey(ctx context.Context, raw string) (*Principal, error)
}

// Hasher turns a presented raw api-key into the digest stored in better-auth's
// `apikey.key` column, so the gateway can look the row up without a callback
// (§4.2). It is an interface so the exact scheme can be swapped if a pinned
// better-auth version changes it — confirmed by the R5 contract test.
type Hasher interface {
	Hash(rawKey string) string
}

// betterAuthSHA256Hasher implements better-auth's DEFAULT api-key hashing.
//
// ASSUMPTION (R5 contract test must confirm against the pinned better-auth
// version): better-auth's api-key plugin `defaultKeyHasher` computes
//
//	base64url( SHA-256(rawKey) )   WITHOUT padding
//
// i.e. `base64Url.encode(SHA-256(utf8(key)), { padding: false })`. Confirmed
// against better-auth v1.6.x source (packages/api-key/src/index.ts). It is NOT
// hex and NOT padded base64. If a future pinned version diverges, swap the
// Hasher (or fall back to RemoteKeyVerifier).
type betterAuthSHA256Hasher struct{}

// DefaultHasher returns the better-auth default api-key Hasher (SHA-256 →
// unpadded base64url).
func DefaultHasher() Hasher { return betterAuthSHA256Hasher{} }

func (betterAuthSHA256Hasher) Hash(rawKey string) string {
	sum := sha256.Sum256([]byte(rawKey))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// keyRepo is the read-only `apikey` lookup the verifier needs (the Stage-2
// store.APIKeyRepo satisfies it). Consumer-defined here so authz depends on an
// abstraction, not the concrete repo.
type keyRepo interface {
	GetByHash(ctx context.Context, keyHash string) (domain.APIKey, error)
}

// APIKeyVerifier validates a presented api-key locally: hash it with the
// configured Hasher, look the row up in the shared `apikey` table, then check
// enabled / expiry / org-scope (§4.2). It builds an org-scoped Principal with the
// key's permissions and no UserID.
type APIKeyVerifier struct {
	repo   keyRepo
	hasher Hasher
	now    func() time.Time
}

// NewAPIKeyVerifier builds a verifier over the given read-only key repo. A nil
// hasher uses the better-auth default (DefaultHasher).
func NewAPIKeyVerifier(repo keyRepo, hasher Hasher) (*APIKeyVerifier, error) {
	if repo == nil {
		return nil, errors.New("authz: api-key repo must not be nil")
	}
	if hasher == nil {
		hasher = DefaultHasher()
	}
	return &APIKeyVerifier{repo: repo, hasher: hasher, now: time.Now}, nil
}

// VerifyKey implements KeyVerifier.
func (v *APIKeyVerifier) VerifyKey(ctx context.Context, raw string) (*Principal, error) {
	if raw == "" {
		return nil, errors.New("authz: empty api-key")
	}
	hash := v.hasher.Hash(raw)
	key, err := v.repo.GetByHash(ctx, hash)
	if err != nil {
		return nil, fmt.Errorf("authz: lookup api-key: %w", err)
	}
	if !key.Enabled {
		return nil, errors.New("authz: api-key disabled")
	}
	if key.ExpiresAt != nil && *key.ExpiresAt <= v.now().UnixMilli() {
		return nil, errors.New("authz: api-key expired")
	}
	if key.OrganizationID == "" {
		// Org-scoped keys only (§4.2): a key with no owning org can't authorize
		// against org-owned resources.
		return nil, errors.New("authz: api-key has no owning organization")
	}
	return &Principal{
		Kind:           KindAPIKey,
		OrganizationID: key.OrganizationID,
		KeyID:          key.ID,
		KeyPermissions: key.Permissions,
	}, nil
}
