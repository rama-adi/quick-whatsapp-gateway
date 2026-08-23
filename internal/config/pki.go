package config

import (
	"encoding/base64"
	"fmt"
	"os"
	"time"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/pki"
)

// PKIConfig is intentionally unwired until the local MySQL signer increment.
type PKIConfig struct {
	EncryptionKey                                                         []byte
	EncryptionKeyID                                                       string
	LeafTTL, ClockSkew, RootTTL, IntermediateTTL, IntermediateRenewBefore time.Duration
}

func LoadPKI() (*PKIConfig, error) { return LoadPKIWith(os.Getenv) }
func LoadPKIWith(getenv func(string) string) (*PKIConfig, error) {
	key, err := base64.StdEncoding.DecodeString(getenv("PKI_ENCRYPTION_KEY"))
	if err != nil {
		return nil, fmt.Errorf("config: decode PKI_ENCRYPTION_KEY: %w", err)
	}
	if base64.StdEncoding.EncodeToString(key) != getenv("PKI_ENCRYPTION_KEY") {
		return nil, fmt.Errorf("config: PKI_ENCRYPTION_KEY must be canonical base64")
	}
	ttl := 24 * time.Hour
	if v := getenv("PKI_LEAF_TTL"); v != "" {
		ttl, err = time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("config: PKI_LEAF_TTL: %w", err)
		}
	}
	skew := 5 * time.Minute
	if v := getenv("PKI_CLOCK_SKEW"); v != "" {
		skew, err = time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("config: PKI_CLOCK_SKEW: %w", err)
		}
	}
	parse := func(name string, fallback time.Duration) (time.Duration, error) {
		if v := getenv(name); v != "" {
			return time.ParseDuration(v)
		}
		return fallback, nil
	}
	rootTTL, err := parse("PKI_ROOT_TTL", 10*365*24*time.Hour)
	if err != nil {
		return nil, fmt.Errorf("config: PKI_ROOT_TTL: %w", err)
	}
	intermediateTTL, err := parse("PKI_INTERMEDIATE_TTL", 90*24*time.Hour)
	if err != nil {
		return nil, fmt.Errorf("config: PKI_INTERMEDIATE_TTL: %w", err)
	}
	renew, err := parse("PKI_INTERMEDIATE_RENEW_BEFORE", 30*24*time.Hour)
	if err != nil {
		return nil, fmt.Errorf("config: PKI_INTERMEDIATE_RENEW_BEFORE: %w", err)
	}
	c := &PKIConfig{
		EncryptionKey:           key,
		EncryptionKeyID:         getenv("PKI_ENCRYPTION_KEY_ID"),
		LeafTTL:                 ttl,
		ClockSkew:               skew,
		RootTTL:                 rootTTL,
		IntermediateTTL:         intermediateTTL,
		IntermediateRenewBefore: renew,
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}
func (c *PKIConfig) Validate() error {
	if len(c.EncryptionKey) != 32 {
		return fmt.Errorf("config: PKI_ENCRYPTION_KEY must decode to 32 bytes")
	}
	if c.EncryptionKeyID == "" {
		return fmt.Errorf("config: PKI_ENCRYPTION_KEY_ID is required")
	}
	if c.LeafTTL <= 0 || c.LeafTTL > 24*time.Hour {
		return fmt.Errorf("config: PKI_LEAF_TTL must be within (0,24h]")
	}
	if c.ClockSkew < 0 || c.ClockSkew > 15*time.Minute {
		return fmt.Errorf("config: PKI_CLOCK_SKEW must be within [0,15m]")
	}
	if c.RootTTL <= 0 || c.IntermediateTTL <= 0 || c.IntermediateTTL >= c.RootTTL {
		return fmt.Errorf("config: invalid authority TTLs")
	}
	if c.IntermediateRenewBefore <= 0 || c.IntermediateRenewBefore >= c.IntermediateTTL {
		return fmt.Errorf("config: invalid intermediate renewal window")
	}
	minimum := c.LeafTTL + c.ClockSkew
	if c.IntermediateTTL <= minimum || c.IntermediateRenewBefore < minimum {
		return fmt.Errorf("config: intermediate TTL and renewal window must cover leaf TTL plus clock skew")
	}
	return nil
}
func (c *PKIConfig) Policy() (pki.Policy, error) { return pki.NewPolicy(c.LeafTTL, c.ClockSkew) }
