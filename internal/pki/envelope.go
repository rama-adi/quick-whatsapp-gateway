package pki

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
)

type KeyBinding struct {
	AuthorityID, Kind, KeyID string
	CertificateFingerprint   []byte
}
type Envelope struct{ Ciphertext, Nonce []byte }

func aad(b KeyBinding) []byte {
	parts := [][]byte{[]byte("quick-wa/pki-key/v1"), []byte(b.AuthorityID), []byte(b.Kind), b.CertificateFingerprint, []byte(b.KeyID)}
	var out []byte
	for _, p := range parts {
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(len(p)))
		out = append(out, size[:]...)
		out = append(out, p...)
	}
	return out
}
func SealKey(key, plaintext []byte, binding KeyBinding, randomness io.Reader) (Envelope, error) {
	if len(key) != 32 {
		return Envelope{}, errors.New("AES-256 key required")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return Envelope{}, errors.New("AES-256 key required")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return Envelope{}, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if randomness == nil {
		randomness = rand.Reader
	}
	if _, err := io.ReadFull(randomness, nonce); err != nil {
		return Envelope{}, err
	}
	return Envelope{Ciphertext: gcm.Seal(nil, nonce, plaintext, aad(binding)), Nonce: nonce}, nil
}
func OpenKey(key []byte, envelope Envelope, binding KeyBinding) ([]byte, error) {
	if len(key) != 32 {
		return nil, errors.New("AES-256 key required")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errors.New("AES-256 key required")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(envelope.Nonce) != gcm.NonceSize() {
		return nil, errors.New("invalid nonce")
	}
	return gcm.Open(nil, envelope.Nonce, envelope.Ciphertext, aad(binding))
}
