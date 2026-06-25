package service

import (
	"context"
	"errors"
	"log/slog"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/crypto"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
)

// KeyService owns the account-global API key lifecycle (§10): create (full key
// shown once), list, get, delete (revoke), and rotate. It also implements the
// middleware APIKeyVerifier used on the authenticated /api/v1 surface.
type KeyService struct {
	keys    *store.APIKeyRepo
	tenants *store.TenantRepo
	log     *slog.Logger
}

// compile-time proof KeyService implements the middleware verifier contract
// (the same shape as middleware.APIKeyVerifier, redeclared here to avoid an
// import cycle through the http middleware package).
var _ interface {
	Verify(ctx context.Context, rawKey string) (*domain.APIKey, *domain.Tenant, error)
} = (*KeyService)(nil)

// NewKeyService constructs a KeyService.
func NewKeyService(keys *store.APIKeyRepo, tenants *store.TenantRepo, log *slog.Logger) *KeyService {
	if log == nil {
		log = slog.Default()
	}
	return &KeyService{keys: keys, tenants: tenants, log: log}
}

// CreateKeyInput is the body of POST /keys.
type CreateKeyInput struct {
	Name        string
	Permissions domain.Permissions
	Scope       domain.APIKeyScope
	ExpiresAt   *int64
}

// CreateKeyResult carries the freshly-minted key. FullKey is the only time the
// secret is ever exposed; only its prefix + argon2id hash are persisted.
type CreateKeyResult struct {
	Key     domain.APIKey `json:"key"`
	FullKey string        `json:"fullKey"`
}

// Create mints a new API key for the tenant. The full key is returned once.
func (s *KeyService) Create(ctx context.Context, tenantID string, in CreateKeyInput) (CreateKeyResult, error) {
	if in.Name == "" {
		return CreateKeyResult{}, domain.ErrValidation("name is required")
	}
	scope := in.Scope
	if scope == "" {
		scope = domain.ScopeTenant
	}
	full, prefix, hash, err := crypto.GenerateAPIKey()
	if err != nil {
		return CreateKeyResult{}, domain.ErrInternal("failed to generate api key")
	}
	now := domain.NowMs()
	k := domain.APIKey{
		ID:          domain.NewAPIKeyID(),
		TenantID:    tenantID,
		Name:        in.Name,
		KeyPrefix:   prefix,
		KeyHash:     hash,
		Scope:       scope,
		Permissions: in.Permissions,
		ExpiresAt:   in.ExpiresAt,
		CreatedAt:   now,
	}
	if err := s.keys.Create(ctx, k); err != nil {
		return CreateKeyResult{}, err
	}
	return CreateKeyResult{Key: k, FullKey: full}, nil
}

// List returns the tenant's keys.
func (s *KeyService) List(ctx context.Context, tenantID string) ([]domain.APIKey, error) {
	return s.keys.ListByTenant(ctx, tenantID)
}

// Get loads one key, enforcing tenant ownership.
func (s *KeyService) Get(ctx context.Context, tenantID, id string) (domain.APIKey, error) {
	k, err := s.keys.Get(ctx, id)
	if err != nil {
		return domain.APIKey{}, err
	}
	if k.TenantID != tenantID {
		return domain.APIKey{}, domain.ErrNotFound("api key not found")
	}
	return k, nil
}

// Delete revokes a key (soft-delete: revoked_at stamped).
func (s *KeyService) Delete(ctx context.Context, tenantID, id string) error {
	if _, err := s.Get(ctx, tenantID, id); err != nil {
		return err
	}
	return s.keys.Revoke(ctx, id, domain.NowMs())
}

// Rotate replaces a key's secret in place, returning the new full key once.
func (s *KeyService) Rotate(ctx context.Context, tenantID, id string) (CreateKeyResult, error) {
	k, err := s.Get(ctx, tenantID, id)
	if err != nil {
		return CreateKeyResult{}, err
	}
	full, prefix, hash, err := crypto.GenerateAPIKey()
	if err != nil {
		return CreateKeyResult{}, domain.ErrInternal("failed to generate api key")
	}
	if err := s.keys.Rotate(ctx, id, prefix, hash); err != nil {
		return CreateKeyResult{}, err
	}
	k.KeyPrefix = prefix
	k.RevokedAt = nil
	k.LastUsedAt = nil
	return CreateKeyResult{Key: k, FullKey: full}, nil
}

// Verify implements middleware.APIKeyVerifier: it parses the lookup prefix from
// the presented key, loads the row, verifies the argon2id hash in constant time,
// rejects revoked/expired keys, loads the owning tenant, and best-effort bumps
// last_used_at. Any failure returns an error (the middleware maps every failure
// to a 401 without leaking which check failed).
func (s *KeyService) Verify(ctx context.Context, rawKey string) (*domain.APIKey, *domain.Tenant, error) {
	prefix, err := crypto.PrefixOf(rawKey)
	if err != nil {
		return nil, nil, domain.ErrUnauthorized("invalid api key")
	}
	k, err := s.keys.GetByPrefix(ctx, prefix)
	if err != nil {
		return nil, nil, domain.ErrUnauthorized("invalid api key")
	}
	if !crypto.VerifyAPIKey(rawKey, k.KeyHash) {
		return nil, nil, domain.ErrUnauthorized("invalid api key")
	}
	now := domain.NowMs()
	if k.RevokedAt != nil {
		return nil, nil, domain.ErrUnauthorized("api key revoked")
	}
	if k.ExpiresAt != nil && *k.ExpiresAt <= now {
		return nil, nil, domain.ErrUnauthorized("api key expired")
	}
	tenant, err := s.tenants.GetByID(ctx, k.TenantID)
	if err != nil {
		// A global-scope key may not have a tenant mirror row; tolerate that by
		// synthesizing a minimal tenant from the key.
		var apiErr *domain.APIError
		if errors.As(err, &apiErr) && apiErr.Code == domain.CodeNotFound {
			tenant = domain.Tenant{ID: k.TenantID}
		} else {
			return nil, nil, domain.ErrUnauthorized("invalid api key")
		}
	}
	// Best-effort last-used stamp; a failure must not reject the request.
	if err := s.keys.UpdateLastUsed(ctx, k.ID, now); err != nil {
		s.log.WarnContext(ctx, "update api key last_used failed", "key_id", k.ID, "err", err)
	}
	k.LastUsedAt = &now
	return &k, &tenant, nil
}
