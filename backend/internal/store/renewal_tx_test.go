package store

import (
	"testing"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
)

func TestRenewalCertificateKindsRemainDisjoint(t *testing.T) {
	token := "tok_1"
	enrollment := domain.GatewayCertificate{
		GatewayID: "gw_1", IssuanceKind: "enrollment", EnrollmentTokenID: &token,
		CSRSHA256: []byte("csr"), NotBefore: 10, NotAfter: 30,
	}
	if !certificateMatches(enrollment, "gw_1", token, []byte("csr"), 20) {
		t.Fatal("valid enrollment certificate rejected")
	}
	enrollment.EnrollmentTokenID = nil
	if certificateMatches(enrollment, "gw_1", token, []byte("csr"), 20) {
		t.Fatal("tokenless enrollment certificate accepted")
	}

	renewal := domain.GatewayCertificate{
		GatewayID: "gw_1", IssuanceKind: "renewal",
		CSRSHA256: []byte("csr"), NotBefore: 10, NotAfter: 30,
	}
	if !newRenewalMatches(renewal, "gw_1", []byte("csr"), 20) {
		t.Fatal("valid renewal certificate rejected")
	}
	renewal.EnrollmentTokenID = &token
	if newRenewalMatches(renewal, "gw_1", []byte("csr"), 20) {
		t.Fatal("token-bound renewal certificate accepted")
	}
}

func TestRenewalReplayRequiresLiveExactCertificate(t *testing.T) {
	cert := domain.GatewayCertificate{
		GatewayID: "gw_1", IssuanceKind: "renewal",
		CSRSHA256: []byte("csr"), NotBefore: 10, NotAfter: 30,
	}
	if !renewalReplayMatches(cert, "gw_1", []byte("csr"), 20) {
		t.Fatal("valid replay rejected")
	}
	revoked := int64(19)
	cert.RevokedAt = &revoked
	if renewalReplayMatches(cert, "gw_1", []byte("csr"), 20) {
		t.Fatal("revoked replay accepted")
	}
	cert.RevokedAt = nil
	if renewalReplayMatches(cert, "gw_1", []byte("other"), 20) ||
		renewalReplayMatches(cert, "gw_1", []byte("csr"), 30) {
		t.Fatal("non-exact or expired replay accepted")
	}
}
