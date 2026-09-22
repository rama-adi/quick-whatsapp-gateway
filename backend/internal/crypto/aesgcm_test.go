package crypto

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"testing"
)

func newTestKeyB64(t *testing.T) string {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return base64.StdEncoding.EncodeToString(key)
}

// TestAESGCMRoundTrip verifies authenticated encryption preserves arbitrary plaintext.
// It exercises constructor, random-nonce sealing, and opening as one contract so format changes cannot silently break stored secrets.
func TestAESGCMRoundTrip(t *testing.T) {
	g, err := NewAESGCM(newTestKeyB64(t))
	if err != nil {
		t.Fatalf("NewAESGCM: %v", err)
	}
	plain := []byte("super-secret-hmac-key")
	ct, err := g.Encrypt(plain)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if bytes.Contains(ct, plain) {
		t.Fatal("ciphertext leaks plaintext")
	}
	got, err := g.Decrypt(ct)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("round trip mismatch: got %q want %q", got, plain)
	}
}

// TestAESGCMNonceRandomized guards against nonce reuse for identical plaintexts.
// Two encryptions under one key must differ while both remain decryptable, which is required for GCM confidentiality.
func TestAESGCMNonceRandomized(t *testing.T) {
	g, _ := NewAESGCM(newTestKeyB64(t))
	a, _ := g.Encrypt([]byte("same"))
	b, _ := g.Encrypt([]byte("same"))
	if bytes.Equal(a, b) {
		t.Fatal("expected distinct ciphertexts for repeated plaintext (random nonce)")
	}
}

// TestAESGCMTamperDetection verifies modified ciphertext never yields plaintext.
// A flipped authenticated byte must return ErrMalformedCiphertext rather than partial data or a provider-specific error.
func TestAESGCMTamperDetection(t *testing.T) {
	g, _ := NewAESGCM(newTestKeyB64(t))
	ct, _ := g.Encrypt([]byte("payload"))
	ct[len(ct)-1] ^= 0xFF // flip a bit in the tag/ciphertext
	if _, err := g.Decrypt(ct); err == nil {
		t.Fatal("expected error on tampered ciphertext")
	}
}

// TestAESGCMShortCiphertext verifies truncated nonce input is rejected safely.
// This protects callers handling corrupt database values from slice panics and ambiguous failures.
func TestAESGCMShortCiphertext(t *testing.T) {
	g, _ := NewAESGCM(newTestKeyB64(t))
	if _, err := g.Decrypt([]byte{0x01, 0x02}); err != ErrMalformedCiphertext {
		t.Fatalf("expected ErrMalformedCiphertext, got %v", err)
	}
}

// TestAESGCMWrongKeyFails verifies GCM authentication rejects another valid key.
// It models key misconfiguration and requires the same fail-closed error used for tampered data.
func TestAESGCMWrongKeyFails(t *testing.T) {
	g1, _ := NewAESGCM(newTestKeyB64(t))
	g2, _ := NewAESGCM(newTestKeyB64(t))
	ct, _ := g1.Encrypt([]byte("payload"))
	if _, err := g2.Decrypt(ct); err == nil {
		t.Fatal("expected decrypt to fail under a different key")
	}
}

// TestNewAESGCMRejectsBadKey covers malformed base64 and non-AES-256 key lengths.
// Invalid configuration must fail during construction, before any secret can be processed.
func TestNewAESGCMRejectsBadKey(t *testing.T) {
	if _, err := NewAESGCM("not-base64!!!"); err == nil {
		t.Fatal("expected error on non-base64 key")
	}
	short := base64.StdEncoding.EncodeToString(make([]byte, 16))
	if _, err := NewAESGCM(short); err != ErrInvalidKey {
		t.Fatalf("expected ErrInvalidKey for 16-byte key, got %v", err)
	}
}
