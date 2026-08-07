// Package sqlitestore is the gateway's WhatsApp device keystore: a thin wrapper
// around go.mau.fi/whatsmeow/store/sqlstore backed by the pure-Go
// modernc.org/sqlite driver (CGO_ENABLED=0).
//
// sqlstore already implements every whatsmeow store interface over SQLite, so
// this package re-implements nothing. It only has to (a) register the modernc
// driver and (b) hand dbutil a dialect string it accepts.
//
// Driver/dialect note: modernc registers under the name "sqlite". dbutil's
// ParseDialect accepts any engine string with the "sqlite" prefix (so "sqlite"
// and "sqlite3" both resolve to dbutil.SQLite). We therefore open the DB with
// sql.Open("sqlite", dsn) and wrap it with NewWithDB(db, "sqlite", ...): no
// driver alias is needed.
package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	// Registers the pure-Go "sqlite" driver. Imported for its side effect only.
	_ "modernc.org/sqlite"

	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// driverName is the database/sql driver registered by modernc.org/sqlite.
const driverName = "sqlite"

// ErrKeystoreMissing means an assigned keystore file was not present (or was
// empty). Callers must not turn this into a fresh pairing request.
var ErrKeystoreMissing = errors.New("keystore_missing")

// ErrKeystoreCorrupt means SQLite could not verify the existing keystore.
var ErrKeystoreCorrupt = errors.New("keystore_corrupt")

// Health is safe to report to the control plane. It deliberately contains no
// database contents or device identifiers.
type Health struct {
	Present             bool
	ByteSize            int64
	Integrity           string
	LastSuccessfulCheck time.Time
}

// Managed is an opened keystore with explicit integrity and shutdown seams.
// Container remains available for callers that need whatsmeow's concrete
// implementation, while Close checkpoints WAL before closing it.
type Managed struct {
	Container *sqlstore.Container
	db        *sql.DB
	health    Health
}

// FilePath extracts a local absolute SQLite file path from a file: DSN. It is
// intentionally stricter than SQLite so private deployments cannot use an
// in-memory, relative, or URI-hosted database as a keystore.
func FilePath(dsn string) (string, error) {
	if !strings.HasPrefix(dsn, "file:") {
		return "", fmt.Errorf("sqlite keystore must use a file: DSN")
	}
	target, query, _ := strings.Cut(strings.TrimPrefix(dsn, "file:"), "?")
	if target == "" || target == ":memory:" || strings.HasPrefix(target, ":memory:") || strings.Contains(query, "mode=memory") {
		return "", fmt.Errorf("sqlite keystore must not use in-memory storage")
	}
	// file://host/path has SQLite URI authority semantics, not a local path.
	if strings.HasPrefix(target, "//") && !strings.HasPrefix(target, "///") {
		return "", fmt.Errorf("sqlite keystore must not use a file URI authority")
	}
	path := filepath.Clean(target)
	if !filepath.IsAbs(path) || path == string(filepath.Separator) {
		return "", fmt.Errorf("sqlite keystore path must be absolute, clean, and non-root")
	}
	return path, nil
}

// Inspect verifies an existing file without creating or upgrading it. A
// successful result is suitable for reporting before desired-state device
// adoption. Missing and corrupt storage use errors.Is with the exported
// classification errors above.
func Inspect(ctx context.Context, dsn string) (Health, error) {
	path, err := FilePath(dsn)
	if err != nil {
		return Health{Integrity: "unavailable"}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Health{Integrity: "missing"}, fmt.Errorf("%w: %s", ErrKeystoreMissing, path)
		}
		return Health{Integrity: "unavailable"}, fmt.Errorf("stat sqlite keystore: %w", err)
	}
	health := Health{Present: true, ByteSize: info.Size(), Integrity: "unknown"}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		health.Integrity = "missing"
		return health, fmt.Errorf("%w: %s is not a non-empty regular file", ErrKeystoreMissing, path)
	}
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		health.Integrity = "corrupt"
		return health, fmt.Errorf("%w: open sqlite keystore: %v", ErrKeystoreCorrupt, err)
	}
	defer func() { _ = db.Close() }()
	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&result); err != nil || result != "ok" {
		health.Integrity = "corrupt"
		if err != nil {
			return health, fmt.Errorf("%w: quick_check: %v", ErrKeystoreCorrupt, err)
		}
		return health, fmt.Errorf("%w: quick_check returned %q", ErrKeystoreCorrupt, result)
	}
	health.Integrity = "ok"
	health.LastSuccessfulCheck = time.Now().UTC()
	return health, nil
}

// Open opens (or creates) the SQLite database at dsn and returns an upgraded
// sqlstore.Container that implements store.DeviceContainer.
//
// Foreign keys are required by sqlstore.Upgrade; callers should include
// "?_pragma=foreign_keys(1)" in the dsn (Open appends it if absent).
func Open(ctx context.Context, dsn string, log waLog.Logger) (*sqlstore.Container, error) {
	if log == nil {
		log = waLog.Noop
	}
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, err
	}
	container := sqlstore.NewWithDB(db, driverName, log)
	if err := container.Upgrade(ctx); err != nil {
		_ = container.Close()
		return nil, fmt.Errorf("failed to upgrade sqlite store: %w", err)
	}
	return container, nil
}

// OpenExisting opens only an already-present, integrity-checked keystore. It
// is the fail-closed API for a gateway that has assigned sessions to adopt.
func OpenExisting(ctx context.Context, dsn string, log waLog.Logger) (*Managed, error) {
	return open(ctx, dsn, log, true)
}

// OpenManaged opens a new or existing keystore and returns lifecycle hooks.
// It preserves Open's create-on-first-use behavior for local development and
// unassigned bootstrap paths.
func OpenManaged(ctx context.Context, dsn string, log waLog.Logger) (*Managed, error) {
	return open(ctx, dsn, log, false)
}

func open(ctx context.Context, dsn string, log waLog.Logger, requireExisting bool) (*Managed, error) {
	if log == nil {
		log = waLog.Noop
	}
	var health Health
	if requireExisting {
		var err error
		health, err = Inspect(ctx, dsn)
		if err != nil {
			return nil, err
		}
	}
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}
	// "sqlite" satisfies dbutil.ParseDialect (prefix match) -> dbutil.SQLite.
	container := sqlstore.NewWithDB(db, driverName, log)
	if err := container.Upgrade(ctx); err != nil {
		_ = container.Close()
		return nil, fmt.Errorf("failed to upgrade sqlite store: %w", err)
	}
	if !requireExisting {
		// New stores are checked after Upgrade so the health data describes the
		// usable database, not a just-created zero-byte file.
		health, err = Inspect(ctx, dsn)
		if err != nil {
			_ = container.Close()
			return nil, err
		}
	}
	return &Managed{Container: container, db: db, health: health}, nil
}

// Health returns the most recent successful integrity check recorded at open.
func (m *Managed) Health() Health { return m.health }

// Close flushes WAL into the main database before closing the container. The
// caller must first quiesce WhatsApp work; checkpointing is not a live backup.
func (m *Managed) Close() error {
	if m == nil || m.Container == nil {
		return nil
	}
	_, checkpointErr := m.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	closeErr := m.Container.Close()
	if checkpointErr != nil && closeErr != nil {
		return errors.Join(fmt.Errorf("checkpoint sqlite wal: %w", checkpointErr), closeErr)
	}
	if checkpointErr != nil {
		return fmt.Errorf("checkpoint sqlite wal: %w", checkpointErr)
	}
	return closeErr
}
