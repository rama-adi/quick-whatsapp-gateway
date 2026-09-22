package enrollmenttoken

import (
	"bytes"
	"errors"
	"testing"
)

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("entropy") }

func TestGenerateParseVerify(t *testing.T) {
	token, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(token.String())
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Selector() != token.Selector() || len(token.String()) != len(prefix)+26+1+43 {
		t.Fatal("non-canonical token")
	}
	digest := Digest(token.String())
	if !Verify(token.String(), digest) || Verify(token.String()+"x", digest) {
		t.Fatal("verification mismatch")
	}
}
func TestGenerateErrors(t *testing.T) {
	if _, e := GenerateWith(errorReader{}, func() string { return "01ARZ3NDEKTSV4RRFFQ69G5FAV" }); e == nil {
		t.Fatal("entropy error ignored")
	}
	if _, e := GenerateWith(bytes.NewReader(make([]byte, 32)), func() string { return "bad" }); e == nil {
		t.Fatal("selector error ignored")
	}
}
func TestParseRejectsNonCanonical(t *testing.T) {
	for _, value := range []string{"", "qwg_enroll_v2_x.y", prefix + "00000000000000000000000000.AA", prefix + "01ARZ3NDEKTSV4RRFFQ69G5FAV.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA."} {
		if _, err := Parse(value); err == nil {
			t.Errorf("accepted %q", value)
		}
	}
}
func FuzzParse(f *testing.F) {
	t, _ := Generate()
	f.Add(t.String())
	f.Add("")
	f.Fuzz(func(t *testing.T, value string) {
		parsed, err := Parse(value)
		if err == nil && parsed.String() != value {
			t.Fatal("not canonical")
		}
	})
}
func FuzzVerify(f *testing.F) {
	token, _ := Generate()
	digest := Digest(token.String())
	f.Add(token.String())
	f.Add("")
	f.Fuzz(func(t *testing.T, s string) {
		ok := Verify(s, digest)
		if ok && s != token.String() {
			t.Fatal("false acceptance")
		}
	})
}
