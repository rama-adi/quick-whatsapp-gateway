package store

import (
	"os"
	"strings"
	"testing"
)

func TestEnrollmentMutationsOnlyThroughAggregate(t *testing.T) {
	b, e := os.ReadFile("gateway_control.go")
	if e != nil {
		t.Fatal(e)
	}
	source := string(b)
	for _, forbidden := range []string{"type EnrollmentTokenRepo", "type GatewayCertificateRepo", "func NewEnrollmentTokenRepo", "func NewGatewayCertificateRepo"} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("public enrollment mutation seam remains: %s", forbidden)
		}
	}
	storeSource, e := os.ReadFile("store.go")
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(storeSource), "EnrollmentTokens") || strings.Contains(string(storeSource), "GatewayCertificates") {
		t.Fatal("general Store exposes enrollment mutation repositories")
	}
	aggregate, e := os.ReadFile("enrollment_tx.go")
	if e != nil {
		t.Fatal(e)
	}
	for _, forbidden := range []string{"type EnrollmentTransaction", "WithEnrollmentTx", "MarkGatewayEnrolled(", "InsertCertificate(", "Finalize(", "LockToken("} {
		if strings.Contains(string(aggregate), forbidden) {
			t.Fatalf("public enrollment primitive/callback seam remains: %s", forbidden)
		}
	}
}
