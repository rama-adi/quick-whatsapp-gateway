package localmysql

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthorityMutationsAreOwnedByTransactionalAdapter(t *testing.T) {
	root := filepath.Join("..", "..", "store")
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, forbidden := range []string{"RotatePKIAuthority", "InsertPKIAuthority", "NewPKIAuthorityRepo"} {
			if strings.Contains(string(b), forbidden) {
				t.Errorf("%s exposes duplicate authority mutation %s", path, forbidden)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
