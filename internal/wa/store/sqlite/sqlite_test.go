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

// TestOpenCreatesDeviceContainer opens a temporary SQLite file with foreign keys enabled through
// the production constructor. It verifies migrations complete and a usable, initially empty whatsmeow
// device container is returned.
func TestOpenCreatesDeviceContainer(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "wa.db")
	dsn := "file:" + dbPath + "?_pragma=foreign_keys(1)"

	c, err := Open(context.Background(), dsn, nil)
	require.NoError(t, err)
	require.NotNil(t, c)
	defer func() { _ = c.Close() }()

	dev, err := c.GetFirstDevice(context.Background())
	require.NoError(t, err)
	require.NotNil(t, dev)
	// Empty DB -> unpaired device (ID nil) with generated keys.
	assert.Nil(t, dev.ID)
	assert.NotNil(t, dev.NoiseKey)
	assert.NotNil(t, dev.IdentityKey)
}

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

func TestOpenExistingReportsHealthAndCheckpointsOnClose(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "wa.db") + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)"
	created, err := OpenManaged(ctx, dsn, nil)
	require.NoError(t, err)
	require.NoError(t, created.Close())

	managed, err := OpenExisting(ctx, dsn, nil)
	require.NoError(t, err)
	health := managed.Health()
	assert.True(t, health.Present)
	assert.Positive(t, health.ByteSize)
	assert.Equal(t, "ok", health.Integrity)
	assert.False(t, health.LastSuccessfulCheck.IsZero())
	require.NoError(t, managed.Close())
}

// TestQuiescedCheckpointedVolumeRestore models the supported operator recovery
// path: close (which checkpoints WAL), then copy the stopped volume's database
// file and verify it before adoption. It intentionally does not copy a live
// WAL-mode main file.
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
