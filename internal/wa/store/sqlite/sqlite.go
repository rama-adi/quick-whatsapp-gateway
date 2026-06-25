// Package sqlitestore is the zero-config WhatsApp keystore fallback. It is a thin
// wrapper around go.mau.fi/whatsmeow/store/sqlstore backed by the pure-Go
// modernc.org/sqlite driver (CGO_ENABLED=0).
//
// WHY a wrapper and not the MySQL hand-rolled store: for single-process,
// self-host deployments sqlstore already implements every store interface over
// SQLite, so there is nothing to re-implement. We only have to (a) make sure the
// modernc driver is registered and (b) hand dbutil a dialect string it accepts.
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
	"fmt"

	// Registers the pure-Go "sqlite" driver. Imported for its side effect only.
	_ "modernc.org/sqlite"

	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// driverName is the database/sql driver registered by modernc.org/sqlite.
const driverName = "sqlite"

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
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}
	// "sqlite" satisfies dbutil.ParseDialect (prefix match) -> dbutil.SQLite.
	container := sqlstore.NewWithDB(db, driverName, log)
	if err := container.Upgrade(ctx); err != nil {
		_ = container.Close()
		return nil, fmt.Errorf("failed to upgrade sqlite store: %w", err)
	}
	return container, nil
}
