package crypto

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// APIKeyPrefix is the human-readable prefix on every generated key ("wak_" =
// WhatsApp API Key). The first PrefixDisplayLen characters of the random body
// form the stored key_prefix used for the auth lookup (see store.APIKeyRepo).
const (
	APIKeyPrefix     = "wak_"
	prefixBodyLen    = 4  // random chars after the prefix used for the lookup prefix
	secretBytesLen   = 24 // random bytes of the full key body (base64url, no padding)
	argon2Time       = 1
	argon2MemoryKiB  = 64 * 1024 // 64 MiB
	argon2Threads    = 4
	argon2KeyLen     = 32
	argon2SaltLen    = 16
	argon2HashScheme = "argon2id"
)

// ErrInvalidAPIKey is returned by VerifyAPIKey-adjacent parsing when a presented
// key is not in the expected "wak_<body>" form.
var ErrInvalidAPIKey = errors.New("crypto: malformed API key")

// GenerateAPIKey mints a new API key. It returns:
//   - fullKey: the secret shown to the user exactly once, "wak_<random>".
//   - prefix:  the non-secret lookup prefix stored in key_prefix, "wak_xxxx".
//   - hash:    the argon2id PHC-format hash stored in key_hash.
//
// The prefix is a deterministic substring of fullKey, so the auth path can parse
// it from the presented key and fetch the row by key_prefix before verifying the
// hash.
func GenerateAPIKey() (fullKey, prefix, hash string, err error) {
	body := make([]byte, secretBytesLen)
	if _, err = rand.Read(body); err != nil {
		return "", "", "", fmt.Errorf("crypto: read random: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(body)
	fullKey = APIKeyPrefix + encoded
	prefix = APIKeyPrefix + encoded[:prefixBodyLen]
	hash, err = hashAPIKey(fullKey)
	if err != nil {
		return "", "", "", err
	}
	return fullKey, prefix, hash, nil
}

// PrefixOf extracts the lookup prefix ("wak_xxxx") from a presented full key. It
// is the inverse of the prefix returned by GenerateAPIKey and is used on the
// auth hot path to fetch the candidate row before hash verification.
func PrefixOf(fullKey string) (string, error) {
	if !strings.HasPrefix(fullKey, APIKeyPrefix) {
		return "", ErrInvalidAPIKey
	}
	body := fullKey[len(APIKeyPrefix):]
	if len(body) < prefixBodyLen {
		return "", ErrInvalidAPIKey
	}
	return APIKeyPrefix + body[:prefixBodyLen], nil
}

// VerifyAPIKey reports whether fullKey matches the stored argon2id hash. It is
// constant-time with respect to the derived key and returns false (never panics)
// on any malformed hash or mismatch.
func VerifyAPIKey(fullKey, hash string) bool {
	salt, want, params, err := parseArgon2Hash(hash)
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(fullKey), salt, params.time, params.memory, params.threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// hashAPIKey derives an argon2id hash of the key and encodes it in the standard
// PHC string format so parameters travel with the hash.
func hashAPIKey(fullKey string) (string, error) {
	salt := make([]byte, argon2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("crypto: read salt: %w", err)
	}
	sum := argon2.IDKey([]byte(fullKey), salt, argon2Time, argon2MemoryKiB, argon2Threads, argon2KeyLen)
	return fmt.Sprintf(
		"$%s$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2HashScheme, argon2.Version,
		argon2MemoryKiB, argon2Time, argon2Threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(sum),
	), nil
}

type argon2Params struct {
	memory  uint32
	time    uint32
	threads uint8
}

// parseArgon2Hash parses the PHC-format argon2id hash produced by hashAPIKey.
func parseArgon2Hash(hash string) (salt, sum []byte, p argon2Params, err error) {
	parts := strings.Split(hash, "$")
	// "" / argon2id / v=.. / m=..,t=..,p=.. / salt / sum
	if len(parts) != 6 || parts[0] != "" || parts[1] != argon2HashScheme {
		return nil, nil, p, ErrInvalidAPIKey
	}
	var version int
	if _, err = fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return nil, nil, p, ErrInvalidAPIKey
	}
	if _, err = fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memory, &p.time, &p.threads); err != nil {
		return nil, nil, p, ErrInvalidAPIKey
	}
	if salt, err = base64.RawStdEncoding.DecodeString(parts[4]); err != nil {
		return nil, nil, p, ErrInvalidAPIKey
	}
	if sum, err = base64.RawStdEncoding.DecodeString(parts[5]); err != nil {
		return nil, nil, p, ErrInvalidAPIKey
	}
	return salt, sum, p, nil
}
