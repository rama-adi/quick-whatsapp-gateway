package authz

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// RemoteKeyVerifier is the DOCUMENTED FALLBACK path (§4.2): if better-auth's
// api-key hash proves non-replicable or version-fragile, the gateway can verify a
// key by calling the supported endpoint
//
//	POST {BETTER_AUTH_URL}/api/auth/api-key/verify   {"key": "<raw>"}
//
// instead of hashing + DB lookup. It is NOT wired by default (the local
// APIKeyVerifier is preferred — it keeps the gateway self-sufficient when the
// frontend is down). Provided so wiring it later is a constructor swap, behind a
// short-TTL cache the caller adds.
type RemoteKeyVerifier struct {
	baseURL string
	client  *http.Client
}

// NewRemoteKeyVerifier builds a fallback verifier against the frontend base URL.
func NewRemoteKeyVerifier(betterAuthURL string, client *http.Client) (*RemoteKeyVerifier, error) {
	if betterAuthURL == "" {
		return nil, errors.New("authz: better-auth url must not be empty")
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &RemoteKeyVerifier{baseURL: strings.TrimRight(betterAuthURL, "/"), client: client}, nil
}

// remoteVerifyResponse is the subset of better-auth's api-key/verify response the
// gateway needs. better-auth returns {valid, error, key:{...}}.
type remoteVerifyResponse struct {
	Valid bool `json:"valid"`
	Key   *struct {
		ID             string              `json:"id"`
		OrganizationID string              `json:"organizationId"`
		Enabled        bool                `json:"enabled"`
		Permissions    map[string][]string `json:"permissions"`
	} `json:"key"`
}

// VerifyKey implements KeyVerifier via the remote endpoint. Permission mapping
// from better-auth's {resource: [actions]} shape is left to the caller's policy
// when this path is adopted; v2 ships the local verifier as primary.
func (v *RemoteKeyVerifier) VerifyKey(ctx context.Context, raw string) (*Principal, error) {
	body, err := json.Marshal(map[string]string{"key": raw})
	if err != nil {
		return nil, fmt.Errorf("authz: marshal remote verify: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		v.baseURL+"/api/auth/api-key/verify", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("authz: build remote verify request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := v.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("authz: remote verify request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("authz: remote verify status %d", resp.StatusCode)
	}
	var out remoteVerifyResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("authz: decode remote verify: %w", err)
	}
	if !out.Valid || out.Key == nil || !out.Key.Enabled || out.Key.OrganizationID == "" {
		return nil, errors.New("authz: remote verify rejected key")
	}
	return &Principal{
		Kind:           KindAPIKey,
		OrganizationID: out.Key.OrganizationID,
		KeyID:          out.Key.ID,
		// Permission shape mapping deferred to adoption (§4.2 fallback).
	}, nil
}
