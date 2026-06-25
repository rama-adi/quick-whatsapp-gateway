package crypto

import (
	"strings"
	"testing"
)

func TestGenerateAPIKeyFormat(t *testing.T) {
	full, prefix, hash, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("GenerateAPIKey: %v", err)
	}
	if !strings.HasPrefix(full, APIKeyPrefix) {
		t.Fatalf("full key %q missing prefix %q", full, APIKeyPrefix)
	}
	if !strings.HasPrefix(prefix, APIKeyPrefix) {
		t.Fatalf("prefix %q missing %q", prefix, APIKeyPrefix)
	}
	if len(prefix) != len(APIKeyPrefix)+prefixBodyLen {
		t.Fatalf("prefix len = %d, want %d", len(prefix), len(APIKeyPrefix)+prefixBodyLen)
	}
	if !strings.HasPrefix(full, prefix) {
		t.Fatalf("prefix %q is not a substring head of full key %q", prefix, full)
	}
	if !strings.HasPrefix(hash, "$"+argon2HashScheme+"$") {
		t.Fatalf("hash not argon2id PHC format: %q", hash)
	}
}

func TestPrefixOfMatchesGenerated(t *testing.T) {
	full, prefix, _, _ := GenerateAPIKey()
	got, err := PrefixOf(full)
	if err != nil {
		t.Fatalf("PrefixOf: %v", err)
	}
	if got != prefix {
		t.Fatalf("PrefixOf = %q, want %q", got, prefix)
	}
}

func TestPrefixOfRejectsMalformed(t *testing.T) {
	for _, bad := range []string{"", "xyz", "wak_", "nope_abcdef"} {
		if _, err := PrefixOf(bad); err == nil {
			t.Fatalf("expected error for %q", bad)
		}
	}
}

func TestVerifyAPIKey(t *testing.T) {
	full, _, hash, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("GenerateAPIKey: %v", err)
	}
	if !VerifyAPIKey(full, hash) {
		t.Fatal("expected valid key to verify")
	}
}

func TestVerifyAPIKeyWrongKeyRejected(t *testing.T) {
	_, _, hash, _ := GenerateAPIKey()
	other, _, _, _ := GenerateAPIKey()
	if VerifyAPIKey(other, hash) {
		t.Fatal("expected a different key to fail verification")
	}
	if VerifyAPIKey(full(t)+"tampered", hash) {
		t.Fatal("expected tampered key to fail verification")
	}
}

func TestVerifyAPIKeyMalformedHash(t *testing.T) {
	full, _, _, _ := GenerateAPIKey()
	for _, bad := range []string{"", "notahash", "$argon2id$v=19$bad", "$bcrypt$x$y$z$w"} {
		if VerifyAPIKey(full, bad) {
			t.Fatalf("expected false for malformed hash %q", bad)
		}
	}
}

func TestGenerateAPIKeyUnique(t *testing.T) {
	seen := map[string]bool{}
	for range 50 {
		full, _, _, err := GenerateAPIKey()
		if err != nil {
			t.Fatalf("GenerateAPIKey: %v", err)
		}
		if seen[full] {
			t.Fatal("duplicate key generated")
		}
		seen[full] = true
	}
}

func full(t *testing.T) string {
	t.Helper()
	f, _, _, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("GenerateAPIKey: %v", err)
	}
	return f
}
