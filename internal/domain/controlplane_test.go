package domain

import (
	"reflect"
	"strings"
	"testing"
)

func TestGatewayStatusValid(t *testing.T) {
	for _, status := range []GatewayStatus{GatewayPendingEnrollment, GatewayJoining, GatewayActive, GatewayDraining, GatewayDrained, GatewayDisabled} {
		if !status.Valid() {
			t.Fatalf("%q should be valid", status)
		}
	}
	if GatewayStatus("unreachable").Valid() {
		t.Fatal("derived unreachable must not be persisted")
	}
}

func TestPersistentTokenTypeHasNoPlaintextSecretField(t *testing.T) {
	typ := reflect.TypeOf(EnrollmentToken{})
	for i := 0; i < typ.NumField(); i++ {
		name := strings.ToLower(typ.Field(i).Name)
		if name == "token" || strings.Contains(name, "plaintext") || strings.Contains(name, "secret") {
			t.Fatalf("persistent token field %q can expose plaintext", typ.Field(i).Name)
		}
	}
}
