package migrations

import (
	"strings"
	"testing"
)

func TestPKISchemaEnforcesSingleActiveAndRotationLock(t *testing.T) {
	b, err := FS.ReadFile("0001_init.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, required := range []string{
		"active_kind            VARCHAR(16) GENERATED ALWAYS AS (CASE WHEN status = 'active' THEN kind ELSE NULL END) STORED",
		"UNIQUE KEY uq_pki_single_active_kind (active_kind)",
		"CONSTRAINT fk_pki_parent FOREIGN KEY (parent_authority_id, parent_kind) REFERENCES pki_authorities(id, kind) ON DELETE RESTRICT",
		"CREATE TABLE pki_rotation_lock",
		"INSERT INTO pki_rotation_lock (id) VALUES (1)",
		"CONSTRAINT fk_gateway_certificate_token FOREIGN KEY (enrollment_token_id) REFERENCES gateway_enrollment_tokens(id) ON DELETE RESTRICT",
		"UNIQUE KEY uq_gateway_certificate_token_csr (enrollment_token_id, csr_sha256)",
		"trust_bundle_pem        MEDIUMTEXT NOT NULL",
	} {
		if !strings.Contains(s, required) {
			t.Fatalf("missing PKI invariant %q", required)
		}
	}
}
