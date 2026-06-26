// Package wastore is the entrypoint for the whatsmeow device keystore: a
// gateway-local, pure-Go modernc.org/sqlite database driven by whatsmeow's own
// sqlstore (masterplan §6.1). It exposes the sqlstore container behind one
// Keystore interface so the session manager depends on the device surface, not
// the concrete sqlstore type.
package wastore

import (
	"context"
	"fmt"

	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"

	sqlitestore "github.com/ramaadi/quick-whatsapp-gateway/internal/wa/store/sqlite"
)

// Keystore is the device-source surface the session manager depends on. The
// sqlstore Container satisfies it. It is a superset of store.DeviceContainer
// (which only declares Put/DeleteDevice) plus the loaders and factory the
// manager needs.
//
// This interface is defined by the consumer (this package) per Go convention.
type Keystore interface {
	GetFirstDevice(ctx context.Context) (*store.Device, error)
	GetAllDevices(ctx context.Context) ([]*store.Device, error)
	GetDevice(ctx context.Context, jid types.JID) (*store.Device, error)
	NewDevice() *store.Device
	PutDevice(ctx context.Context, device *store.Device) error
	DeleteDevice(ctx context.Context, device *store.Device) error
}

// Open builds the gateway-local SQLite keystore: it opens (and upgrades) the
// SQLite file at dsn via whatsmeow's sqlstore. dsn must be non-empty
// (WHATSMEOW_STORE_DSN).
func Open(ctx context.Context, dsn string, log waLog.Logger) (Keystore, error) {
	if dsn == "" {
		return nil, fmt.Errorf("wastore: sqlite keystore requires a non-empty dsn")
	}
	return sqlitestore.Open(ctx, dsn, log)
}
