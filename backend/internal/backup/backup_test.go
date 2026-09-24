package backup

import (
	"bytes"
	"compress/zlib"
	"crypto/aes"
	"crypto/cipher"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPBFieldBytesRejectsMalformedFields covers bounds and field-number checks on hostile headers.
// Table cases exercise zero tags, truncated fixed fields, and overflowing lengths; each must return an error without panicking.
func TestPBFieldBytesRejectsMalformedFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		buf  []byte
	}{
		{"field zero", []byte{0x00}},
		{"truncated fixed64", []byte{0x09, 1, 2}},
		{"truncated fixed32", []byte{0x0d, 1, 2}},
		{"oversized length", []byte{0x0a, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := pbFieldBytes(tt.buf, 1); err == nil {
				t.Fatal("expected malformed protobuf error")
			}
		})
	}
}

// TestOpenEscapesPathAndFingerprintIncludesColumns protects read-only URI handling and schema drift detection.
// A fixture with URL metacharacters must open immutably, and adding a probed column must change its fingerprint.
func TestOpenEscapesPathAndFingerprintIncludesColumns(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fixturePath := filepath.Join(dir, "fixture.db")
	path := filepath.Join(dir, "msg store?#.db")
	raw, err := sql.Open("sqlite", fixturePath)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE message (id INTEGER); CREATE TABLE chat (id INTEGER); CREATE TABLE jid (id INTEGER)`); err != nil {
		_ = raw.Close()
		t.Fatalf("create fixture: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}
	if err := os.Rename(fixturePath, path); err != nil {
		t.Fatalf("rename fixture: %v", err)
	}

	first, err := Open(path)
	if err != nil {
		t.Fatalf("Open path containing URL metacharacters: %v", err)
	}
	fingerprint1 := first.Fingerprint()
	if _, err := first.db.Exec("ALTER TABLE message ADD COLUMN body TEXT"); err == nil {
		_ = first.Close()
		t.Fatal("immutable backup handle accepted a write")
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first reader: %v", err)
	}

	writableDSN := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=rw"}).String()
	raw, err = sql.Open("sqlite", writableDSN)
	if err != nil {
		t.Fatalf("reopen fixture: %v", err)
	}
	if _, err := raw.Exec("ALTER TABLE message ADD COLUMN body TEXT"); err != nil {
		_ = raw.Close()
		t.Fatalf("alter fixture: %v", err)
	}
	_ = raw.Close()

	second, err := Open(path)
	if err != nil {
		t.Fatalf("reopen backup: %v", err)
	}
	defer func() { _ = second.Close() }()
	if fingerprint2 := second.Fingerprint(); fingerprint2 == fingerprint1 {
		t.Fatalf("fingerprint did not change after column addition: %q", fingerprint1)
	} else if !strings.Contains(fingerprint2, "caps=") {
		t.Fatalf("malformed fingerprint: %q", fingerprint2)
	}
}

// makeCrypt15 builds a valid single-file crypt15 container around plaintext,
// mirroring the real on-disk layout: [size byte][optional 0x01 flag][protobuf
// header with c15_iv.IV] | ciphertext | 16-byte GCM tag | 16-byte checksum.
func makeCrypt15(t *testing.T, rootKey, plaintext []byte, featureFlag bool) []byte {
	t.Helper()
	// Compress like a real msgstore (zlib).
	var zbuf bytes.Buffer
	zw := zlib.NewWriter(&zbuf)
	if _, err := zw.Write(plaintext); err != nil {
		t.Fatalf("zlib write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zlib close: %v", err)
	}

	aesKey := DeriveAESKey(rootKey)
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	gcm, err := cipher.NewGCMWithNonceSize(block, 16)
	if err != nil {
		t.Fatalf("gcm: %v", err)
	}

	iv := bytes.Repeat([]byte{0x42}, 16)
	sealed := gcm.Seal(nil, iv, zbuf.Bytes(), nil) // ciphertext || tag

	// BackupPrefix protobuf: field 3 (c15_iv, LEN) { field 1 (IV, LEN) = 16 bytes }.
	ivField := append([]byte{0x0A, 0x10}, iv...)                   // tag=field1/LEN, len=16
	header := append([]byte{0x1A, byte(len(ivField))}, ivField...) // tag=field3/LEN

	checksum := bytes.Repeat([]byte{0x00}, 16) // not verified (GCM tag authenticates)

	file := []byte{byte(len(header))}
	if featureFlag {
		file = append(file, 0x01)
	}
	file = append(file, header...)
	file = append(file, sealed...)
	file = append(file, checksum...)
	return file
}

// TestDecryptMsgstore_RoundTrip covers both valid crypt15 header layouts end to end.
// Synthetic encrypted SQLite bytes are parsed, authenticated, and decompressed with and without the feature flag.
func TestDecryptMsgstore_RoundTrip(t *testing.T) {
	rootKey := bytes.Repeat([]byte{0x01}, 32)
	// A real decrypted msgstore starts with the SQLite magic.
	plaintext := append([]byte("SQLite format 3\x00"), bytes.Repeat([]byte("payload-"), 64)...)

	for _, flag := range []bool{false, true} {
		file := makeCrypt15(t, rootKey, plaintext, flag)
		got, err := DecryptMsgstore(file, hex.EncodeToString(rootKey))
		if err != nil {
			t.Fatalf("DecryptMsgstore(featureFlag=%v): %v", flag, err)
		}
		if !bytes.Equal(got, plaintext) {
			t.Fatalf("round-trip mismatch (featureFlag=%v): got %d bytes", flag, len(got))
		}
	}
}

// TestDecryptMsgstore_WrongKey verifies GCM authentication fails closed.
// A structurally valid backup encrypted under another root key must never produce importable bytes.
func TestDecryptMsgstore_WrongKey(t *testing.T) {
	rootKey := bytes.Repeat([]byte{0x01}, 32)
	plaintext := append([]byte("SQLite format 3\x00"), []byte("x")...)
	file := makeCrypt15(t, rootKey, plaintext, true)

	wrong := hex.EncodeToString(bytes.Repeat([]byte{0x02}, 32))
	if _, err := DecryptMsgstore(file, wrong); err == nil {
		t.Fatal("expected error with wrong key, got nil")
	}
}

// TestLoadRootKey covers every supported key encoding and malformed input.
// Hex, whitespace-wrapped hex, raw bytes, and Java serialization must converge on the same 32-byte root key.
func TestLoadRootKey(t *testing.T) {
	raw := bytes.Repeat([]byte{0xAB}, 32)

	// 64-char hex.
	if k, err := LoadRootKey([]byte(hex.EncodeToString(raw))); err != nil || !bytes.Equal(k, raw) {
		t.Fatalf("hex key: %v / equal=%v", err, bytes.Equal(k, raw))
	}
	// hex with surrounding whitespace.
	if k, err := LoadRootKey([]byte("  " + hex.EncodeToString(raw) + "\n")); err != nil || !bytes.Equal(k, raw) {
		t.Fatalf("hex key with ws: %v", err)
	}
	// raw 32 bytes.
	if k, err := LoadRootKey(raw); err != nil || !bytes.Equal(k, raw) {
		t.Fatalf("raw key: %v", err)
	}
	// java-serialized: prefix AC ED ... then final 32 bytes are the key.
	java := append([]byte{0xAC, 0xED, 0x00, 0x05, 0x75, 0x72}, raw...)
	if k, err := LoadRootKey(java); err != nil || !bytes.Equal(k, raw) {
		t.Fatalf("java key: %v", err)
	}
	// garbage.
	if _, err := LoadRootKey([]byte("nope")); err == nil {
		t.Fatal("expected error for garbage key")
	}
}
