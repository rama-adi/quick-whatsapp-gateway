package backup

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/hex"
	"os"
	"testing"
)

// makeCrypt15 builds a valid single-file crypt15 container around plaintext,
// mirroring the on-disk layout Decrypt expects: 122-byte header (16-byte nonce at
// offset 8) | ciphertext | 16-byte GCM tag | 16-byte MD5 checksum.
func makeCrypt15(t *testing.T, rootKey, plaintext []byte) []byte {
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

	header := make([]byte, defaultDataOffset)
	nonce := bytes.Repeat([]byte{0x42}, 16)
	copy(header[defaultIVOffset:], nonce)

	sealed := gcm.Seal(nil, nonce, zbuf.Bytes(), nil) // ciphertext || tag
	h := md5.New()
	h.Write(header)
	h.Write(sealed)
	checksum := h.Sum(nil)

	file := append([]byte{}, header...)
	file = append(file, sealed...)
	file = append(file, checksum...)
	return file
}

func TestDecryptMsgstore_RoundTrip(t *testing.T) {
	rootKey := bytes.Repeat([]byte{0x01}, 32)
	// A real decrypted msgstore starts with the SQLite magic.
	plaintext := append([]byte("SQLite format 3\x00"), bytes.Repeat([]byte("payload-"), 64)...)

	file := makeCrypt15(t, rootKey, plaintext)

	got, err := DecryptMsgstore(file, hex.EncodeToString(rootKey))
	if err != nil {
		t.Fatalf("DecryptMsgstore: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("round-trip mismatch: got %d bytes", len(got))
	}
}

func TestDecryptMsgstore_WrongKey(t *testing.T) {
	rootKey := bytes.Repeat([]byte{0x01}, 32)
	plaintext := append([]byte("SQLite format 3\x00"), []byte("x")...)
	file := makeCrypt15(t, rootKey, plaintext)

	wrong := hex.EncodeToString(bytes.Repeat([]byte{0x02}, 32))
	if _, err := DecryptMsgstore(file, wrong); err == nil {
		t.Fatal("expected error with wrong key, got nil")
	}
}

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

func TestCoarseType(t *testing.T) {
	cases := map[int]string{
		0: "text", 1: "image", 42: "image", 3: "video", 43: "video",
		13: "gif", 2: "audio", 9: "document", 15: "sticker", 20: "sticker",
		4: "contact", 5: "location", 16: "location", 7: "", 99: "",
	}
	for code, want := range cases {
		if got := coarseType(code); got != want {
			t.Errorf("coarseType(%d) = %q, want %q", code, got, want)
		}
	}
}

func TestChatTypeForServer(t *testing.T) {
	cases := map[string]string{
		"s.whatsapp.net": "dm", "lid": "dm", "g.us": "group",
		"newsletter": "newsletter", "broadcast": "broadcast", "status_me": "status",
	}
	for server, want := range cases {
		if got := chatTypeForServer(server); got != want {
			t.Errorf("chatTypeForServer(%q) = %q, want %q", server, got, want)
		}
	}
}

// sampleDBPath is the dev-only decrypted msgstore used to validate the reader.
// The test skips when it is absent (CI), so it never gates the build.
const sampleDBPath = "../../web/msgstore.db"

func TestReadSampleMsgstore(t *testing.T) {
	if _, err := os.Stat(sampleDBPath); err != nil {
		t.Skipf("sample DB not present (%s); skipping", sampleDBPath)
	}
	db, err := Open(sampleDBPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	t.Logf("schema fingerprint: %s", db.Fingerprint())

	ctx := context.Background()
	var chats, msgs, ids, groups, members int
	if err := db.EachChat(ctx, func(c Chat) error {
		if c.JID != "" {
			chats++
		}
		return nil
	}); err != nil {
		t.Fatalf("EachChat: %v", err)
	}
	if err := db.EachMessage(ctx, func(m Message) error {
		if m.WAMessageID != "" && m.ChatJID != "" {
			msgs++
		}
		return nil
	}); err != nil {
		t.Fatalf("EachMessage: %v", err)
	}
	if err := db.EachIdentity(ctx, func(Identity) error { ids++; return nil }); err != nil {
		t.Fatalf("EachIdentity: %v", err)
	}
	if err := db.EachGroup(ctx, func(Group) error { groups++; return nil }); err != nil {
		t.Fatalf("EachGroup: %v", err)
	}
	if err := db.EachGroupMember(ctx, func(GroupMember) error { members++; return nil }); err != nil {
		t.Fatalf("EachGroupMember: %v", err)
	}

	t.Logf("chats=%d messages=%d identities=%d groups=%d members=%d", chats, msgs, ids, groups, members)
	if chats == 0 || msgs == 0 || ids == 0 || groups == 0 {
		t.Fatalf("expected non-zero imports, got chats=%d messages=%d identities=%d groups=%d", chats, msgs, ids, groups)
	}
}
