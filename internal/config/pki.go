package config

import (
	"encoding/base64"
	"fmt"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/pki"
	"os"
	"time"
)

// PKIConfig is intentionally unwired until the local MySQL signer increment.
type PKIConfig struct {
	EncryptionKey      []byte
	EncryptionKeyID    string
	LeafTTL, ClockSkew time.Duration
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
	c := &PKIConfig{EncryptionKey: key, EncryptionKeyID: getenv("PKI_ENCRYPTION_KEY_ID"), LeafTTL: ttl, ClockSkew: skew}
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
	return nil
}
func (c *PKIConfig) Policy() (pki.Policy, error) { return pki.NewPolicy(c.LeafTTL, c.ClockSkew) }
