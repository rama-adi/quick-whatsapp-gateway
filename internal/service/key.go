package service

import (
	"context"
	"log/slog"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
)

// KeyHasher hashes a presented raw api-key into the digest stored in better-auth's
// `apikey.key` column, so the gateway can look the row up. better-auth hashes keys
// deterministically by default; the concrete implementation (which MUST match the
// pinned better-auth version's scheme) is supplied by the authz package in a later
// stage. Defining it as an interface here keeps the hashing scheme in one place and
// lets the verifier be unit-tested with a fake hasher.
type KeyHasher interface {
	HashKey(rawKey string) (string, error)
}

// KeyVerifier is the gateway's READ-ONLY better-auth api-key verifier (§4.2). It
// no longer owns any key lifecycle (create/list/revoke/rotate moved to the
// frontend's better-auth api-key plugin, §6). Given a presented raw key it hashes
// it, looks up the row in the shared `apikey` table, checks enabled/expiry, and
// resolves the owning organization id. It implements middleware.APIKeyVerifier.
type KeyVerifier struct {
	keys   *store.APIKeyRepo
	hasher KeyHasher
	log    *slog.Logger
}

// compile-time proof KeyVerifier implements the middleware verifier contract (the
// same shape as middleware.APIKeyVerifier, redeclared here to avoid an import
// cycle through the http middleware package).
var _ interface {
	Verify(ctx context.Context, rawKey string) (*domain.APIKey, string, error)
} = (*KeyVerifier)(nil)

// NewKeyVerifier constructs a KeyVerifier over the read-only apikey repo and the
// better-auth key hasher. hasher may be nil only in tests that never call Verify.
func NewKeyVerifier(keys *store.APIKeyRepo, hasher KeyHasher, log *slog.Logger) *KeyVerifier {
	if log == nil {
		log = slog.Default()
	}
	return &KeyVerifier{keys: keys, hasher: hasher, log: log}
}

// Verify implements middleware.APIKeyVerifier: it hashes the presented key with
// better-auth's scheme, loads the `apikey` row by that hash, rejects
// disabled/expired keys, and returns the key plus its owning organization id. Any
// failure returns an error (the middleware maps every failure to a 401 without
// leaking which check failed).
func (v *KeyVerifier) Verify(ctx context.Context, rawKey string) (*domain.APIKey, string, error) {
	if v.hasher == nil {
		return nil, "", domain.ErrInternal("api key hasher not configured")
	}
	hash, err := v.hasher.HashKey(rawKey)
	if err != nil {
		return nil, "", domain.ErrUnauthorized("invalid api key")
	}
	k, err := v.keys.GetByHash(ctx, hash)
	if err != nil {
		return nil, "", domain.ErrUnauthorized("invalid api key")
	}
	if !k.Enabled {
		return nil, "", domain.ErrUnauthorized("api key disabled")
	}
	now := domain.NowMs()
	if k.ExpiresAt != nil && *k.ExpiresAt <= now {
		return nil, "", domain.ErrUnauthorized("api key expired")
	}
	if k.OrganizationID == "" {
		// A key with no organizationId cannot own org-scoped resources (§4.2).
		return nil, "", domain.ErrUnauthorized("api key not bound to an organization")
	}
	// Best-effort last-request stamp; a failure must not reject the request.
	if err := v.keys.TouchLastRequest(ctx, k.ID, now); err != nil {
		v.log.WarnContext(ctx, "touch api key lastRequest failed", "key_id", k.ID, "err", err)
	}
	k.LastUsedAt = &now
	return &k, k.OrganizationID, nil
}
