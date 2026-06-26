package wastore

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenSQLite(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "wa.db") + "?_pragma=foreign_keys(1)"
	ks, err := Open(context.Background(), dsn, nil)
	require.NoError(t, err)
	require.NotNil(t, ks)
	dev, err := ks.GetFirstDevice(context.Background())
	require.NoError(t, err)
	assert.Nil(t, dev.ID)
}

func TestOpenSQLiteAndUseKeystore(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "wa.db") + "?_pragma=foreign_keys(1)"
	ks, err := Open(context.Background(), dsn, nil)
	require.NoError(t, err)
	require.NotNil(t, ks)
	// NewDevice is local (no DB) -> exercises the Keystore surface.
	assert.NotNil(t, ks.NewDevice())
}

func TestOpenSQLiteRequiresDSN(t *testing.T) {
	_, err := Open(context.Background(), "", nil)
	require.Error(t, err)
}
