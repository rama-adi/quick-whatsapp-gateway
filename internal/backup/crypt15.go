// Package backup decrypts and reads a WhatsApp encrypted database backup
// (msgstore.db.crypt15) so its chat history can be imported into the gateway's
// own tables. It is pure stdlib (CGO-free): crypt15.go handles the AES-GCM
// decryption and zlib decompression, msgstore.go reads the resulting SQLite via
// modernc.org/sqlite. Nothing here imports internal/* — callers translate the
// errors into their own envelopes.
package backup

import (
	"bytes"
	"compress/zlib"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

// backupEncryptionInfo is the HKDF-style info string WhatsApp mixes into the
// crypt15 key derivation.
const backupEncryptionInfo = "backup encryption\x01"

// Default crypt15 single-file offsets: the 16-byte GCM nonce starts at 8, the
// encrypted payload at 122. These are stable for msgstore.db.crypt15 backups.
const (
	defaultIVOffset   = 8
	defaultDataOffset = 122
)

// DecryptMsgstore is the orchestrator: it derives the AES key from the provided
// crypt15 key, decrypts the ciphertext, and zlib-decompresses the result into a
// raw SQLite database. key accepts a 64-char hex string, a raw 32-byte key, or a
// WhatsApp Java-serialized encrypted_backup.key (see LoadRootKey). Any failure is
// returned as a plain error — the caller maps it to a validation/4xx envelope,
// since a failure here means a bad file or wrong key.
func DecryptMsgstore(ciphertext []byte, key string) ([]byte, error) {
	rootKey, err := LoadRootKey([]byte(key))
	if err != nil {
		return nil, fmt.Errorf("load key: %w", err)
	}
	aesKey := DeriveAESKey(rootKey)

	plain, err := Decrypt(ciphertext, aesKey, defaultIVOffset, defaultDataOffset)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}

	plain, err = Decompress(plain)
	if err != nil {
		return nil, fmt.Errorf("decompress: %w", err)
	}
	if !bytes.HasPrefix(plain, []byte("SQLite format 3")) {
		return nil, errors.New("decrypted output is not a SQLite database (wrong key?)")
	}
	return plain, nil
}

// LoadRootKey accepts the crypt15 key in any of the forms WhatsApp/users produce:
//  1. a 64-character hex string (the form shown in WhatsApp's "end-to-end
//     encrypted backup" key screen),
//  2. a raw 32-byte key,
//  3. a Java-serialized encrypted_backup.key file (the root key is the final 32
//     bytes after the serialization header).
func LoadRootKey(keyArg []byte) ([]byte, error) {
	// Trim incidental whitespace/newlines so a pasted hex key still matches.
	trimmed := bytes.TrimSpace(keyArg)

	if len(trimmed) == 64 {
		if b, err := hex.DecodeString(string(trimmed)); err == nil && len(b) == 32 {
			return b, nil
		}
	}
	if len(trimmed) == 32 {
		return trimmed, nil
	}
	// Java serialized byte[] format:
	// AC ED 00 05 75 72 ... 78 70 00 00 00 20 <32 bytes>. The root key is the
	// final 32 bytes.
	if len(trimmed) > 32 && bytes.HasPrefix(trimmed, []byte{0xAC, 0xED}) {
		return trimmed[len(trimmed)-32:], nil
	}
	return nil, fmt.Errorf("unrecognized key format: got %d bytes", len(trimmed))
}

// DeriveAESKey derives the AES-256 key from the 32-byte root key using the same
// two-step HMAC-SHA256 chain WhatsApp uses for crypt15.
func DeriveAESKey(rootKey []byte) []byte {
	zeroKey := make([]byte, 32)

	mac1 := hmac.New(sha256.New, zeroKey)
	mac1.Write(rootKey)
	step1 := mac1.Sum(nil)

	mac2 := hmac.New(sha256.New, step1)
	mac2.Write([]byte(backupEncryptionInfo))
	return mac2.Sum(nil)
}

// Decrypt unwraps the crypt15 container: a header, a 16-byte GCM nonce at
// ivOffset, then at dataOffset the AES-GCM payload. The normal single-file layout
// is ciphertext | 16-byte GCM tag | 16-byte MD5 checksum (the checksum is
// verified over header+ciphertext+tag); a multifile layout omits the checksum.
func Decrypt(file, aesKey []byte, ivOffset, dataOffset int) ([]byte, error) {
	if ivOffset < 0 || ivOffset+16 > len(file) {
		return nil, errors.New("invalid IV offset")
	}
	if dataOffset <= ivOffset+16 || dataOffset >= len(file) {
		return nil, errors.New("invalid data offset")
	}

	nonce := file[ivOffset : ivOffset+16]
	header := file[:dataOffset]
	payload := file[dataOffset:]

	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCMWithNonceSize(block, 16)
	if err != nil {
		return nil, err
	}

	// Single-file layout with trailing MD5 checksum.
	if len(payload) >= 32 {
		ciphertext := payload[:len(payload)-32]
		tag := payload[len(payload)-32 : len(payload)-16]
		checksum := payload[len(payload)-16:]

		h := md5.New()
		h.Write(header)
		h.Write(ciphertext)
		h.Write(tag)
		if hmac.Equal(h.Sum(nil), checksum) {
			ciphertextWithTag := append(append([]byte{}, ciphertext...), tag...)
			return gcm.Open(nil, nonce, ciphertextWithTag, nil)
		}
	}

	// Multifile layout (no checksum): ciphertext | 16-byte GCM tag.
	if len(payload) >= 16 {
		return gcm.Open(nil, nonce, payload, nil)
	}
	return nil, errors.New("payload too small")
}

// Decompress zlib-inflates the decrypted payload. A backup that is already raw
// (SQLite or ZIP) is returned unchanged.
func Decompress(data []byte) ([]byte, error) {
	r, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		if bytes.HasPrefix(data, []byte("SQLite format 3")) || bytes.HasPrefix(data, []byte("PK\x03\x04")) {
			return data, nil
		}
		return nil, fmt.Errorf("zlib open failed (decryption may have failed): %w", err)
	}
	defer r.Close()
	return io.ReadAll(r)
}
