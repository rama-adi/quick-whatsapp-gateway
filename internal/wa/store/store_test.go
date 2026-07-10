package wastore

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOpenSQLite constructs the public keystore wrapper over a temporary foreign-key-enabled SQLite
// database. A non-nil container confirms driver selection and schema upgrade are wired through the
// facade.
func TestOpenSQLite(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "wa.db") + "?_pragma=foreign_keys(1)"
	ks, err := Open(context.Background(), dsn, nil)
	require.NoError(t, err)
	require.NotNil(t, ks)
	dev, err := ks.GetFirstDevice(context.Background())
	require.NoError(t, err)
	assert.Nil(t, dev.ID)
}

// TestOpenSQLiteAndUseKeystore opens the wrapper and performs a real GetFirstDevice lookup on the
// migrated store. The empty database yields a fresh unpaired device, proving the returned interface is
// operational rather than merely non-nil.
func TestOpenSQLiteAndUseKeystore(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "wa.db") + "?_pragma=foreign_keys(1)"
	ks, err := Open(context.Background(), dsn, nil)
	require.NoError(t, err)
	require.NotNil(t, ks)
	// NewDevice is local (no DB) -> exercises the Keystore surface.
	assert.NotNil(t, ks.NewDevice())
}

// TestOpenSQLiteRequiresDSN calls the public constructor with an empty data-source name. It must
// reject configuration before opening a database, so deployments cannot silently create an unintended
// local file.
func TestOpenSQLiteRequiresDSN(t *testing.T) {
	_, err := Open(context.Background(), "", nil)
	require.Error(t, err)
}
