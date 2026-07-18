package service

import (
	"os"
	"strings"
	"testing"
)

func TestEnrollmentServiceHasNoRawPersistence(t *testing.T) {
	b, e := os.ReadFile("enrollment.go")
	if e != nil {
		t.Fatal(e)
	}
	source := string(b)
	for _, forbidden := range []string{"ExecContext(", "QueryRowContext(", "BeginTx("} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("service bypasses enrollment transaction aggregate with %s", forbidden)
		}
	}
}
