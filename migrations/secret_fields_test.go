package migrations

import (
	"io/fs"
	"strings"
	"testing"
)

func TestControlPlaneSchemaHasNoPlaintextSecretColumns(t *testing.T) {
	b, err := fs.ReadFile(FS, "0001_init.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	schema := strings.ToLower(string(b))
	for _, forbidden := range []string{"plaintext_token", "token_plaintext", "private_key_pem", "private_key_plaintext"} {
		if strings.Contains(schema, forbidden) {
			t.Fatalf("forbidden secret column %q", forbidden)
		}
	}
	if !strings.Contains(schema, "token_hash") || !strings.Contains(schema, "encrypted_private_key") {
		t.Fatal("expected hashed token and encrypted key columns")
	}
}
