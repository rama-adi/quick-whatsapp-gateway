package webhooks

import (
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"testing"
)

// TestSignHMAC_KnownVector signs a published HMAC-SHA512 test vector with a fixed key and message. The
// lowercase hexadecimal output must match the known digest exactly. This catches algorithm, encoding, or
// byte-order changes that external webhook consumers could not tolerate.
func TestSignHMAC_KnownVector(t *testing.T) {
	// Known vector: HMAC-SHA512 of "The quick brown fox jumps over the lazy dog"
	// keyed with "key". Value is the canonical published vector.
	secret := []byte("key")
	body := []byte("The quick brown fox jumps over the lazy dog")
	const want = "b42af09057bac1e2d41708e48a902e09b5ff7f12ab428a4fe86653c73dd248fb82f948a549f7b791a5b41915ee4d1ec3935357e4e2317250d0372afa2ebeeb3a"

	if got := SignHMAC(secret, body); got != want {
		t.Fatalf("SignHMAC mismatch:\n got=%s\nwant=%s", got, want)
	}
}

// TestSignHMAC_MatchesStdlib computes the same signature through SignHMAC and a direct crypto/hmac plus
// sha512 implementation. Byte-for-byte equality proves the helper signs the unmodified payload with the
// standard construction. It guards refactors around encoding without relying only on an internal expected
// value.
func TestSignHMAC_MatchesStdlib(t *testing.T) {
	secret := []byte("super-secret")
	body := []byte(`{"schema":"v1","event":"message"}`)

	mac := hmac.New(sha512.New, secret)
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))

	if got := SignHMAC(secret, body); got != want {
		t.Fatalf("SignHMAC != stdlib hmac-sha512: got=%s want=%s", got, want)
	}
}

// TestSignHMAC_DistinctBodies signs two different payloads under the same secret. Their signatures must
// differ, confirming the body bytes participate in authentication. This prevents accidental signing of a
// constant, empty, or pre-normalized value.
func TestSignHMAC_DistinctBodies(t *testing.T) {
	secret := []byte("k")
	if SignHMAC(secret, []byte("a")) == SignHMAC(secret, []byte("b")) {
		t.Fatal("different bodies produced the same signature")
	}
}
