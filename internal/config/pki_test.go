package config

import (
	"encoding/base64"
	"testing"
)

func TestLoadPKIWith(t *testing.T) {
	values := map[string]string{"PKI_ENCRYPTION_KEY": base64.StdEncoding.EncodeToString(make([]byte, 32)), "PKI_ENCRYPTION_KEY_ID": "k1"}
	c, err := LoadPKIWith(func(k string) string { return values[k] })
	if err != nil || len(c.EncryptionKey) != 32 {
		t.Fatalf("config=%+v err=%v", c, err)
	}
	p, err := c.Policy()
	if err != nil || p.TTL != c.LeafTTL || p.Skew != c.ClockSkew {
		t.Fatal("config policy not wired")
	}
	values["PKI_LEAF_TTL"] = "25h"
	if _, err := LoadPKIWith(func(k string) string { return values[k] }); err == nil {
		t.Fatal("accepted excessive TTL")
	}
}
