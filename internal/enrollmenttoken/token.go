package enrollmenttoken

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
	"strings"

	"github.com/oklog/ulid/v2"
)

const prefix = "qwg_enroll_v1_"
const digestDomain = "quick-wa/enrollment-token/v1\x00"

var ErrInvalid = errors.New("invalid enrollment token")

type Token struct{ selector, value string }

func (t Token) String() string   { return t.value }
func (t Token) Selector() string { return t.selector }

func Generate() (Token, error) {
	return GenerateWith(rand.Reader, func() string { return ulid.Make().String() })
}
func GenerateWith(randomness io.Reader, selector func() string) (Token, error) {
	secret := make([]byte, 32)
	if _, err := io.ReadFull(randomness, secret); err != nil {
		return Token{}, err
	}
	id := selector()
	if parsed, err := ulid.ParseStrict(id); err != nil || parsed.String() != id {
		return Token{}, ErrInvalid
	}
	value := prefix + id + "." + base64.RawURLEncoding.EncodeToString(secret)
	return Token{selector: id, value: value}, nil
}

func Parse(value string) (Token, error) {
	if !strings.HasPrefix(value, prefix) {
		return Token{}, ErrInvalid
	}
	rest := strings.TrimPrefix(value, prefix)
	selector, encoded, ok := strings.Cut(rest, ".")
	if !ok || strings.Contains(encoded, ".") || len(selector) != 26 || len(encoded) != 43 {
		return Token{}, ErrInvalid
	}
	id, err := ulid.ParseStrict(selector)
	if err != nil || id.String() != selector {
		return Token{}, ErrInvalid
	}
	secret, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || len(secret) != 32 || base64.RawURLEncoding.EncodeToString(secret) != encoded {
		return Token{}, ErrInvalid
	}
	return Token{selector: selector, value: value}, nil
}

func Digest(value string) [32]byte {
	return sha256.Sum256(append([]byte(digestDomain), []byte(value)...))
}

// Verify performs the same digest and comparison for malformed input, avoiding
// a cheap validity oracle. Callers return one generic authentication failure.
func Verify(value string, expected [32]byte) bool {
	parsed, err := Parse(value)
	var material string
	valid := 1
	if err != nil {
		material = prefix + "00000000000000000000000000.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
		valid = 0
	} else {
		material = parsed.value
	}
	actual := Digest(material)
	return subtle.ConstantTimeCompare(actual[:], expected[:])&valid == 1
}
