// Package backup decrypts and reads a WhatsApp encrypted database backup
// (msgstore.db.crypt15) so its chat history can be imported into the gateway's
// own tables. It is pure stdlib (CGO-free): crypt15.go handles the AES-GCM
// decryption and zlib decompression, msgstore.go reads the resulting SQLite via
// modernc.org/sqlite. Nothing here imports internal/* — callers translate the
// errors into their own envelopes.
//
// crypt15 file layout (per ElDavoo/wa-crypt-tools):
//
//	[1 byte: protobuf header size N]
//	[optional 1 byte: 0x01 feature-table flag, present on msgstore backups]
//	[N bytes: BackupPrefix protobuf — carries the 16-byte AES-GCM IV in c15_iv.IV]
//	[ciphertext .. | 16-byte GCM tag | 16-byte file checksum]
//
// The AES-256 key is HKDF-derived from the root key with info "backup encryption"
// (the trailing 0x01 is the HKDF block counter). The header is NOT GCM AAD; the
// trailing 16-byte checksum is stripped before GCM (a multifile backup omits it,
// in which case the last 16 bytes are the tag — handled as a fallback).
package backup

import (
	"bytes"
	"compress/zlib"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

// backupEncryptionInfo is the HKDF info string WhatsApp mixes into the crypt15
// key derivation (the 0x01 is the HKDF-Expand block counter for the single 32-byte
// output block).
const backupEncryptionInfo = "backup encryption\x01"

// protobuf field numbers in the crypt15 BackupPrefix header (proto from
// wa-crypt-tools): BackupPrefix.c15_iv is field 3; C15_IV.IV is field 1.
const (
	fieldC15IV = 3
	fieldIV    = 1
)

// DecryptMsgstore is the orchestrator: it parses the crypt15 header, derives the
// AES key, AES-GCM-decrypts the payload using the IV from the header, and
// zlib-decompresses the result into a raw SQLite database. key accepts a 64-char
// hex string, a raw 32-byte key, or a serialized encrypted_backup.key (see
// LoadRootKey). Any failure is returned as a plain error — the caller maps it to a
// validation/4xx envelope, since a failure here means a bad file or wrong key.
func DecryptMsgstore(file []byte, key string) ([]byte, error) {
	rootKey, err := LoadRootKey([]byte(key))
	if err != nil {
		return nil, fmt.Errorf("load key: %w", err)
	}
	aesKey := DeriveAESKey(rootKey)

	header, payload, err := parseCrypt15(file)
	if err != nil {
		return nil, fmt.Errorf("parse header: %w", err)
	}
	iv, err := extractIV(header)
	if err != nil {
		return nil, fmt.Errorf("iv: %w", err)
	}

	plain, err := gcmDecrypt(payload, aesKey, iv)
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

// DeriveAESKey derives the AES-256 key from the 32-byte root key using WhatsApp's
// HKDF-SHA256 chain (zero salt, info "backup encryption"). Equivalent to
// wa-crypt-tools' encryptionloop with a single 32-byte output block.
func DeriveAESKey(rootKey []byte) []byte {
	zeroKey := make([]byte, 32)

	prk := hmac.New(sha256.New, zeroKey) // HKDF-Extract
	prk.Write(rootKey)
	step1 := prk.Sum(nil)

	expand := hmac.New(sha256.New, step1) // HKDF-Expand, first block
	expand.Write([]byte(backupEncryptionInfo))
	return expand.Sum(nil)
}

// parseCrypt15 splits a crypt15 file into its protobuf header and encrypted
// payload. The first byte is the header size; an optional 0x01 feature-table flag
// may precede the header on msgstore backups.
func parseCrypt15(file []byte) (header, payload []byte, err error) {
	if len(file) < 2 {
		return nil, nil, errors.New("file too short")
	}
	size := int(file[0])
	off := 1
	// A 0x01 here is the msgstore feature-table flag; consume it. A real protobuf
	// header starts with a field tag (>= 0x08), so 0x01 is unambiguous.
	if file[off] == 0x01 {
		off++
	}
	if size <= 0 || off+size > len(file) {
		return nil, nil, fmt.Errorf("protobuf header size %d exceeds file", size)
	}
	header = file[off : off+size]
	payload = file[off+size:]
	if len(payload) < 16 {
		return nil, nil, errors.New("encrypted payload too short")
	}
	return header, payload, nil
}

// extractIV pulls the 16-byte AES-GCM IV out of the BackupPrefix protobuf header
// (c15_iv.IV).
func extractIV(header []byte) ([]byte, error) {
	c15iv, err := pbFieldBytes(header, fieldC15IV)
	if err != nil {
		return nil, err
	}
	iv, err := pbFieldBytes(c15iv, fieldIV)
	if err != nil {
		return nil, err
	}
	if len(iv) != 16 {
		return nil, fmt.Errorf("IV is %d bytes, want 16", len(iv))
	}
	return iv, nil
}

// gcmDecrypt AES-GCM-decrypts the crypt15 payload. The standard single-file
// layout is ciphertext|tag|checksum, so the trailing 16-byte checksum is dropped
// and GCM verifies the tag; a multifile backup omits the checksum (last 16 bytes
// are then the tag) — tried as a fallback.
func gcmDecrypt(payload, aesKey, iv []byte) ([]byte, error) {
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCMWithNonceSize(block, len(iv))
	if err != nil {
		return nil, err
	}
	// Single-file: strip the trailing 16-byte file checksum; payload[:-16] is the
	// ciphertext+tag GCM expects.
	if len(payload) >= 16+gcm.Overhead() {
		if pt, err := gcm.Open(nil, iv, payload[:len(payload)-16], nil); err == nil {
			return pt, nil
		}
	}
	// Multifile: no checksum, the last 16 bytes are the GCM tag.
	pt, err := gcm.Open(nil, iv, payload, nil)
	if err != nil {
		return nil, fmt.Errorf("cipher: %w", err)
	}
	return pt, nil
}

// pbFieldBytes returns the bytes of the first length-delimited (wire type 2) field
// with the given field number in a protobuf message, skipping other fields. Only
// the wire types that can appear in the crypt15 header are handled.
func pbFieldBytes(buf []byte, field int) ([]byte, error) {
	i := 0
	for i < len(buf) {
		tag, n := binary.Uvarint(buf[i:])
		if n <= 0 {
			return nil, errors.New("malformed protobuf tag")
		}
		i += n
		fieldNum := int(tag >> 3)
		wire := int(tag & 0x7)
		switch wire {
		case 0: // varint
			_, n := binary.Uvarint(buf[i:])
			if n <= 0 {
				return nil, errors.New("malformed protobuf varint")
			}
			i += n
		case 1: // 64-bit
			i += 8
		case 5: // 32-bit
			i += 4
		case 2: // length-delimited
			l, n := binary.Uvarint(buf[i:])
			if n <= 0 {
				return nil, errors.New("malformed protobuf length")
			}
			i += n
			if i+int(l) > len(buf) {
				return nil, errors.New("protobuf length out of range")
			}
			if fieldNum == field {
				return buf[i : i+int(l)], nil
			}
			i += int(l)
		default:
			return nil, fmt.Errorf("unsupported protobuf wire type %d", wire)
		}
	}
	return nil, fmt.Errorf("protobuf field %d not found", field)
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
