package wastore

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenMySQLRequiresDB(t *testing.T) {
	_, err := Open(context.Background(), DriverMySQL, nil, "", nil)
	require.Error(t, err)
}

func TestOpenMySQLReturnsKeystore(t *testing.T) {
	db, _, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	ks, err := Open(context.Background(), DriverMySQL, db, "", nil)
	require.NoError(t, err)
	require.NotNil(t, ks)
	// NewDevice is local (no DB) -> exercises the Keystore surface.
	assert.NotNil(t, ks.NewDevice())
}

func TestOpenSQLite(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "wa.db") + "?_pragma=foreign_keys(1)"
	ks, err := Open(context.Background(), DriverSQLite, nil, dsn, nil)
	require.NoError(t, err)
	require.NotNil(t, ks)
	dev, err := ks.GetFirstDevice(context.Background())
	require.NoError(t, err)
	assert.Nil(t, dev.ID)
}

func TestOpenSQLiteAcceptsSqlite3Alias(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "wa.db") + "?_pragma=foreign_keys(1)"
	ks, err := Open(context.Background(), Driver("sqlite3"), nil, dsn, nil)
	require.NoError(t, err)
	require.NotNil(t, ks)
}

func TestOpenUnknownDriver(t *testing.T) {
	_, err := Open(context.Background(), Driver("postgres"), nil, "", nil)
	require.Error(t, err)
}

func TestOpenSQLiteRequiresDSN(t *testing.T) {
	_, err := Open(context.Background(), DriverSQLite, nil, "", nil)
	require.Error(t, err)
}
