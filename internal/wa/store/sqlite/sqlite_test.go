package sqlitestore

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOpenCreatesDeviceContainer opens a temporary SQLite file with foreign keys enabled through
// the production constructor. It verifies migrations complete and a usable, initially empty whatsmeow
// device container is returned.
func TestOpenCreatesDeviceContainer(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "wa.db")
	dsn := "file:" + dbPath + "?_pragma=foreign_keys(1)"

	c, err := Open(context.Background(), dsn, nil)
	require.NoError(t, err)
	require.NotNil(t, c)
	defer c.Close()

	dev, err := c.GetFirstDevice(context.Background())
	require.NoError(t, err)
	require.NotNil(t, dev)
	// Empty DB -> unpaired device (ID nil) with generated keys.
	assert.Nil(t, dev.ID)
	assert.NotNil(t, dev.NoiseKey)
	assert.NotNil(t, dev.IdentityKey)
}

// TestOpenRejectsMissingForeignKeys opens the same store without the required SQLite foreign-key
// option. Initialization must fail during upgrade, making an unsafe keystore configuration impossible
// to start.
func TestOpenRejectsMissingForeignKeys(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "wa.db")
	// No foreign_keys pragma -> sqlstore.Upgrade refuses.
	dsn := "file:" + dbPath
	_, err := Open(context.Background(), dsn, nil)
	require.Error(t, err)
}
