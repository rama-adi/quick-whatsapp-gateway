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
		"active_slot            TINYINT GENERATED ALWAYS AS (CASE WHEN status = 'active' THEN 1 ELSE NULL END) STORED",
		"UNIQUE KEY uq_pki_single_active (active_slot)",
		"CREATE TABLE pki_rotation_lock",
		"INSERT INTO pki_rotation_lock (id) VALUES (1)",
	} {
		if !strings.Contains(s, required) {
			t.Fatalf("missing PKI invariant %q", required)
		}
	}
}
