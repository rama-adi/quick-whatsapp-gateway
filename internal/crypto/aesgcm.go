// Package crypto holds the application's symmetric encryption (AES-GCM) for
// sensitive config/secrets at rest, plus API-key generation and verification
// (argon2id). See masterplan §10.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

// ErrInvalidKey is returned when APP_ENCRYPTION_KEY is not a base64-encoded
// 32-byte value (AES-256).
var ErrInvalidKey = errors.New("crypto: APP_ENCRYPTION_KEY must be base64-encoded 32 bytes")

// ErrMalformedCiphertext is returned when a ciphertext is too short to contain a
// nonce, or fails GCM authentication (tampered, truncated, or wrong key).
var ErrMalformedCiphertext = errors.New("crypto: malformed or tampered ciphertext")

// AESGCM encrypts and decrypts opaque byte blobs with AES-256-GCM. The random
// 12-byte nonce is prepended to each ciphertext, so Encrypt output is
// nonce||sealed and Decrypt expects the same layout. Safe for concurrent use.
type AESGCM struct {
	aead cipher.AEAD
}

// NewAESGCM builds an AESGCM from a base64-encoded 32-byte key (the standard
// encoding of APP_ENCRYPTION_KEY). It returns ErrInvalidKey if the decoded key
// is not exactly 32 bytes.
func NewAESGCM(base64Key string) (*AESGCM, error) {
	key, err := base64.StdEncoding.DecodeString(base64Key)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidKey, err)
	}
	return NewAESGCMFromKey(key)
}

// NewAESGCMFromKey builds an AESGCM from raw key bytes, which must be exactly 32
// bytes (AES-256).
func NewAESGCMFromKey(key []byte) (*AESGCM, error) {
	if len(key) != 32 {
		return nil, ErrInvalidKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("crypto: new cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: new gcm: %w", err)
	}
	return &AESGCM{aead: aead}, nil
}

// Encrypt seals plaintext and returns nonce||ciphertext. A fresh random nonce is
// generated per call, so encrypting the same plaintext twice yields different
// outputs.
func (a *AESGCM) Encrypt(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, a.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("crypto: read nonce: %w", err)
	}
	// Seal appends the ciphertext to nonce, giving nonce||ciphertext.
	return a.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Decrypt reverses Encrypt: it splits off the prepended nonce and opens the
// remainder. Returns ErrMalformedCiphertext if the input is too short or fails
// authentication (tampering or wrong key).
func (a *AESGCM) Decrypt(ciphertext []byte) ([]byte, error) {
	ns := a.aead.NonceSize()
	if len(ciphertext) < ns {
		return nil, ErrMalformedCiphertext
	}
	nonce, sealed := ciphertext[:ns], ciphertext[ns:]
	plaintext, err := a.aead.Open(nil, nonce, sealed, nil)
	if err != nil {
		return nil, ErrMalformedCiphertext
	}
	return plaintext, nil
}
