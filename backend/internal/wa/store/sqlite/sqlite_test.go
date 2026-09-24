package sqlitestore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenExistingClassifiesMissingAndCorruptKeystores(t *testing.T) {
	ctx := context.Background()
	missingDSN := "file:" + filepath.Join(t.TempDir(), "missing.db") + "?_pragma=foreign_keys(1)"
	_, err := OpenExisting(ctx, missingDSN, nil)
	require.ErrorIs(t, err, ErrKeystoreMissing)

	corruptPath := filepath.Join(t.TempDir(), "corrupt.db")
	require.NoError(t, os.WriteFile(corruptPath, []byte("not sqlite"), 0o600))
	health, err := Inspect(ctx, "file:"+corruptPath+"?_pragma=foreign_keys(1)")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrKeystoreCorrupt))
	assert.True(t, health.Present)
	assert.Equal(t, "corrupt", health.Integrity)
}

func TestQuiescedCheckpointedVolumeRestore(t *testing.T) {
	ctx := context.Background()
	sourcePath := filepath.Join(t.TempDir(), "source", "wa.db")
	require.NoError(t, os.MkdirAll(filepath.Dir(sourcePath), 0o700))
	sourceDSN := "file:" + sourcePath + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)"
	managed, err := OpenManaged(ctx, sourceDSN, nil)
	require.NoError(t, err)
	require.NoError(t, managed.Close())

	backup, err := os.ReadFile(sourcePath)
	require.NoError(t, err)
	restoredPath := filepath.Join(t.TempDir(), "restored", "wa.db")
	require.NoError(t, os.MkdirAll(filepath.Dir(restoredPath), 0o700))
	require.NoError(t, os.WriteFile(restoredPath, backup, 0o600))
	restored, err := OpenExisting(ctx, "file:"+restoredPath+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)", nil)
	require.NoError(t, err)
	require.NoError(t, restored.Close())
}

func TestFilePathRejectsNonPersistentForms(t *testing.T) {
	for _, dsn := range []string{"file:store.db", "file::memory:", "file:/tmp/store.db?mode=memory", "file://host/store.db"} {
		_, err := FilePath(dsn)
		assert.Error(t, err, dsn)
	}
	path, err := FilePath("file:/data/keystore/store.db?_pragma=foreign_keys(1)")
	require.NoError(t, err)
	assert.Equal(t, "/data/keystore/store.db", path)
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
