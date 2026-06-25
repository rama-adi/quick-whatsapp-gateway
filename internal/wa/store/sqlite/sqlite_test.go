package sqlitestore

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOpenUpgradesAndServesDevice verifies the modernc/sqlite driver is wired,
// migrations run, and GetFirstDevice returns a fresh (unpaired) device on an
// empty DB. Uses a real file DB (foreign keys must be enabled for Upgrade).
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

func TestOpenRejectsMissingForeignKeys(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "wa.db")
	// No foreign_keys pragma -> sqlstore.Upgrade refuses.
	dsn := "file:" + dbPath
	_, err := Open(context.Background(), dsn, nil)
	require.Error(t, err)
}
