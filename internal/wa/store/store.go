// Package wastore is the backend-agnostic entrypoint for the whatsmeow device
// keystore. It selects between the hand-rolled MySQL backend (internal/wa/store/mysql)
// and the sqlstore-backed SQLite fallback (internal/wa/store/sqlite) based on the
// WHATSMEOW_STORE_DRIVER setting, and exposes both behind one Keystore interface
// so the session manager never has to know which backend is in use.
package wastore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"

	mysqlstore "github.com/ramaadi/quick-whatsapp-gateway/internal/wa/store/mysql"
	sqlitestore "github.com/ramaadi/quick-whatsapp-gateway/internal/wa/store/sqlite"
)

// Driver enumerates the supported keystore backends.
type Driver string

const (
	DriverMySQL  Driver = "mysql"
	DriverSQLite Driver = "sqlite"
)

// Keystore is the device-source surface the session manager depends on. Both the
// MySQL Container and the sqlstore Container satisfy it. It is a superset of
// store.DeviceContainer (which only declares Put/DeleteDevice) plus the loaders
// and factory the manager needs.
//
// This interface is defined by the consumer (this package) per Go convention;
// Phase 3 wires the concrete container in via Open.
type Keystore interface {
	GetFirstDevice(ctx context.Context) (*store.Device, error)
	GetAllDevices(ctx context.Context) ([]*store.Device, error)
	GetDevice(ctx context.Context, jid types.JID) (*store.Device, error)
	NewDevice() *store.Device
	PutDevice(ctx context.Context, device *store.Device) error
	DeleteDevice(ctx context.Context, device *store.Device) error
}

// Compile-time proof that both backends satisfy Keystore. (sqlstore.Container
// is checked indirectly in Open's return path.)
var _ Keystore = (*mysqlstore.Container)(nil)

// Open builds a Keystore for the given driver.
//
//   - DriverMySQL: wraps an existing *sql.DB (the gateway's primary MySQL pool).
//     The caller MUST pass a non-nil db and MUST have applied
//     migrations/0002_wmstore.up.sql beforehand. dsn is ignored.
//   - DriverSQLite: opens (and upgrades) the SQLite file at dsn via sqlstore.
//     db is ignored.
//
// Passing the db is what keeps this package free of an import on internal/store:
// Phase 3 owns the pool and hands it in.
func Open(ctx context.Context, driver Driver, db *sql.DB, dsn string, log waLog.Logger) (Keystore, error) {
	switch normalizeDriver(driver) {
	case DriverMySQL:
		if db == nil {
			return nil, fmt.Errorf("mysql keystore requires a non-nil *sql.DB")
		}
		return mysqlstore.NewContainer(db, log), nil
	case DriverSQLite:
		if dsn == "" {
			return nil, fmt.Errorf("sqlite keystore requires a non-empty dsn")
		}
		c, err := sqlitestore.Open(ctx, dsn, log)
		if err != nil {
			return nil, err
		}
		return c, nil
	default:
		return nil, fmt.Errorf("unknown whatsmeow store driver %q (want mysql or sqlite)", driver)
	}
}

func normalizeDriver(d Driver) Driver {
	switch Driver(strings.ToLower(string(d))) {
	case DriverMySQL:
		return DriverMySQL
	case DriverSQLite, "sqlite3":
		return DriverSQLite
	default:
		return d
	}
}
