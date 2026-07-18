package storedb

import (
	"strings"
	"testing"
)

func TestEnrollmentQueriesUseSelectorAndOwnedLeaseCAS(t *testing.T) {
	where := strings.ToLower(getGatewayEnrollmentTokenForUpdate[strings.Index(strings.ToLower(getGatewayEnrollmentTokenForUpdate), "where"):])
	if strings.Contains(where, "token_hash") || !strings.Contains(getGatewayEnrollmentTokenForUpdate, "WHERE id = ? FOR UPDATE") {
		t.Fatal("token lookup must lock by non-secret selector id")
	}
	for name, query := range map[string]string{"begin": beginGatewayEnrollment, "finalize": finalizeGatewayEnrollment, "release": releaseGatewayEnrollment} {
		if !strings.Contains(query, "redemption_nonce") || !strings.Contains(query, "csr_sha256") {
			t.Fatalf("%s lacks ownership fencing", name)
		}
	}
	if !strings.Contains(finalizeGatewayEnrollment, "lease_expires_at>?") || !strings.Contains(releaseGatewayEnrollment, "lease_expires_at>?") {
		t.Fatal("finalize/release permit work after the ownership lease boundary")
	}
	if !strings.Contains(beginGatewayEnrollment, "lease_expires_at<=?") || !strings.Contains(beginGatewayEnrollment, "attempt_count<max_attempts") {
		t.Fatal("begin lacks expired-lease and bounded-attempt CAS")
	}
	if !strings.Contains(lockGatewayForEnrollment, "FOR UPDATE") || !strings.Contains(revokeLiveGatewayEnrollmentTokens, "status IN ('active','redeeming')") || !strings.Contains(enrollPendingGateway, "status='pending_enrollment'") {
		t.Fatal("gateway enrollment transaction primitives weakened")
	}
	if !strings.Contains(getGatewayCertificateByTokenCSR, "enrollment_token_id=? AND csr_sha256=?") || !strings.Contains(insertGatewayCertificate, "trust_bundle_pem") {
		t.Fatal("certificate replay material is not exact and token-bound")
	}
	if !strings.Contains(lockGatewayEnrollmentToken, "attempt_count=max_attempts") {
		t.Fatal("lock does not exhaust attempts explicitly")
	}
}
