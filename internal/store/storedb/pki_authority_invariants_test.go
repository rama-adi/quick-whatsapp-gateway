package storedb

import (
	"strings"
	"testing"
)

func TestPKIRotationQueriesSerializeAndLoadDeterministically(t *testing.T) {
	if !strings.Contains(lockPKIRotation, "WHERE id=1 FOR UPDATE") {
		t.Fatal("rotation does not serialize on the stable singleton")
	}
	if !strings.Contains(getActivePKIAuthorityForUpdate, "status='active'") ||
		!strings.Contains(getActivePKIAuthorityForUpdate, "ORDER BY created_at DESC LIMIT 1 FOR UPDATE") {
		t.Fatal("active authority load is not locked and deterministic")
	}
	where := getActivePKIAuthorityForRotation[strings.Index(getActivePKIAuthorityForRotation, "WHERE"):]
	if strings.Contains(where, "not_after") || !strings.Contains(where, "FOR UPDATE") {
		t.Fatal("rotation lookup must lock the active authority regardless of expiry")
	}
}
