// Package mysqlstore is a hand-written implementation of whatsmeow's device
// keystore backed by MySQL.
//
// WHY hand-written: go.mau.fi/util/dbutil only knows the Postgres and SQLite
// dialects, and go.mau.fi/whatsmeow/store/sqlstore branches on those two. There
// is no MySQL path through sqlstore, so to persist whatsmeow device state in the
// gateway's primary MySQL database we re-implement every store interface using
// database/sql directly: ON DUPLICATE KEY UPDATE upserts, "?" placeholders, and
// VARBINARY/BLOB columns for the raw key material. Tables are namespaced
// wmstore_* (see migrations/0002_wmstore.up.sql) so they coexist with the app's
// own tables.
package mysqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	mathRand "math/rand/v2"

	"github.com/google/uuid"

	"go.mau.fi/util/random"
	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/util/keys"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// ErrInvalidLength mirrors sqlstore.ErrInvalidLength: the database returned a
// byte slice whose length does not match the expected fixed-size key.
var ErrInvalidLength = errors.New("database returned byte array with illegal length")

// ErrDeviceIDMustBeSet matches sqlstore: a device can't be persisted before its
// JID (assigned at pair time) is known.
var ErrDeviceIDMustBeSet = errors.New("device JID must be known before accessing database")

// Container is the MySQL-backed implementation of store.DeviceContainer. It owns
// the *sql.DB and a process-wide LID map (LIDStore is global, not per-device).
type Container struct {
	db  *sql.DB
	log waLog.Logger
	lid *lidMap
}

var _ store.DeviceContainer = (*Container)(nil)

// NewContainer wraps an existing MySQL *sql.DB. The caller owns the DB lifecycle
// and is responsible for running migrations/0002_wmstore.up.sql beforehand.
func NewContainer(db *sql.DB, log waLog.Logger) *Container {
	if log == nil {
		log = waLog.Noop
	}
	return &Container{
		db:  db,
		log: log,
		lid: newLIDMap(db),
	}
}

const getAllDevicesQuery = `
SELECT jid, lid, registration_id, noise_key, identity_key,
       signed_pre_key, signed_pre_key_id, signed_pre_key_sig,
       adv_key, adv_details, adv_account_sig, adv_account_sig_key, adv_device_sig,
       platform, business_name, push_name, facebook_uuid, lid_migration_ts
FROM wmstore_device
`

const getDeviceQuery = getAllDevicesQuery + " WHERE jid=?"

func (c *Container) scanDevice(row scannable) (*store.Device, error) {
	var device store.Device
	device.Log = c.log
	device.SignedPreKey = &keys.PreKey{}
	var noisePriv, identityPriv, preKeyPriv, preKeySig []byte
	var account waAdv.ADVSignedDeviceIdentity
	// facebook_uuid is stored as TEXT (CHAR(36)) and may be NULL.
	var fbUUID sql.NullString

	err := row.Scan(
		&device.ID, &device.LID, &device.RegistrationID, &noisePriv, &identityPriv,
		&preKeyPriv, &device.SignedPreKey.KeyID, &preKeySig,
		&device.AdvSecretKey, &account.Details, &account.AccountSignature, &account.AccountSignatureKey, &account.DeviceSignature,
		&device.Platform, &device.BusinessName, &device.PushName, &fbUUID, &device.LIDMigrationTimestamp)
	if err != nil {
		return nil, fmt.Errorf("failed to scan device: %w", err)
	} else if len(noisePriv) != 32 || len(identityPriv) != 32 || len(preKeyPriv) != 32 || len(preKeySig) != 64 {
		return nil, ErrInvalidLength
	}

	device.NoiseKey = keys.NewKeyPairFromPrivateKey(*(*[32]byte)(noisePriv))
	device.IdentityKey = keys.NewKeyPairFromPrivateKey(*(*[32]byte)(identityPriv))
	device.SignedPreKey.KeyPair = *keys.NewKeyPairFromPrivateKey(*(*[32]byte)(preKeyPriv))
	device.SignedPreKey.Signature = (*[64]byte)(preKeySig)
	device.Account = &account
	if fbUUID.Valid && fbUUID.String != "" {
		parsed, perr := uuid.Parse(fbUUID.String)
		if perr != nil {
			return nil, fmt.Errorf("failed to parse facebook_uuid: %w", perr)
		}
		device.FacebookUUID = parsed
	}

	c.initializeDevice(&device)
	return &device, nil
}

// GetAllDevices returns every paired device in the database.
func (c *Container) GetAllDevices(ctx context.Context) ([]*store.Device, error) {
	rows, err := c.db.QueryContext(ctx, getAllDevicesQuery)
	if err != nil {
		return nil, fmt.Errorf("failed to query devices: %w", err)
	}
	defer rows.Close()
	var devices []*store.Device
	for rows.Next() {
		dev, serr := c.scanDevice(rows)
		if serr != nil {
			return nil, serr
		}
		devices = append(devices, dev)
	}
	return devices, rows.Err()
}

// GetFirstDevice returns the first device, or a fresh NewDevice() if the DB is
// empty (matching sqlstore semantics: never nil, no error on empty).
func (c *Container) GetFirstDevice(ctx context.Context) (*store.Device, error) {
	devices, err := c.GetAllDevices(ctx)
	if err != nil {
		return nil, err
	}
	if len(devices) == 0 {
		return c.NewDevice(), nil
	}
	return devices[0], nil
}

// GetDevice finds the device with the given (usually AD) JID, or nil if absent.
func (c *Container) GetDevice(ctx context.Context, jid types.JID) (*store.Device, error) {
	dev, err := c.scanDevice(c.db.QueryRowContext(ctx, getDeviceQuery, jid))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return dev, err
}

const (
	insertDeviceQuery = `
		INSERT INTO wmstore_device (jid, lid, registration_id, noise_key, identity_key,
		                            signed_pre_key, signed_pre_key_id, signed_pre_key_sig,
		                            adv_key, adv_details, adv_account_sig, adv_account_sig_key, adv_device_sig,
		                            platform, business_name, push_name, facebook_uuid, lid_migration_ts)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
			lid=VALUES(lid),
			platform=VALUES(platform),
			business_name=VALUES(business_name),
			push_name=VALUES(push_name),
			lid_migration_ts=VALUES(lid_migration_ts)
	`
	deleteDeviceQuery = `DELETE FROM wmstore_device WHERE jid=?`
)

// NewDevice creates a new in-memory device with freshly generated keys. Nothing
// is persisted until PutDevice/Save is called (whatsmeow calls Save after a
// successful pairing).
func (c *Container) NewDevice() *store.Device {
	device := &store.Device{
		Log:       c.log,
		Container: c,

		NoiseKey:       keys.NewKeyPair(),
		IdentityKey:    keys.NewKeyPair(),
		RegistrationID: mathRand.Uint32(),
		AdvSecretKey:   random.Bytes(32),
	}
	device.SignedPreKey = device.IdentityKey.CreateSignedPreKey(1)
	return device
}

// PutDevice upserts the device's identity/key material. Called via Device.Save().
func (c *Container) PutDevice(ctx context.Context, device *store.Device) error {
	if device.ID == nil {
		return ErrDeviceIDMustBeSet
	}
	var fbUUID any
	if device.FacebookUUID != uuid.Nil {
		fbUUID = device.FacebookUUID.String()
	}
	_, err := c.db.ExecContext(ctx, insertDeviceQuery,
		device.ID, device.LID, device.RegistrationID, device.NoiseKey.Priv[:], device.IdentityKey.Priv[:],
		device.SignedPreKey.Priv[:], device.SignedPreKey.KeyID, device.SignedPreKey.Signature[:],
		device.AdvSecretKey, device.Account.Details, device.Account.AccountSignature, device.Account.AccountSignatureKey, device.Account.DeviceSignature,
		device.Platform, device.BusinessName, device.PushName, fbUUID,
		device.LIDMigrationTimestamp,
	)
	if err != nil {
		return fmt.Errorf("failed to upsert device: %w", err)
	}
	if !device.Initialized {
		c.initializeDevice(device)
	}
	return nil
}

// DeleteDevice removes the device (cascades to all per-device tables via FK).
func (c *Container) DeleteDevice(ctx context.Context, device *store.Device) error {
	if device.ID == nil {
		return ErrDeviceIDMustBeSet
	}
	_, err := c.db.ExecContext(ctx, deleteDeviceQuery, device.ID)
	if err != nil {
		return fmt.Errorf("failed to delete device: %w", err)
	}
	return nil
}

// initializeDevice wires the per-device + global stores onto the Device, matching
// sqlstore.Container.initializeDevice.
func (c *Container) initializeDevice(device *store.Device) {
	inner := newMysqlStore(c.db, c.log, *device.ID)
	device.SetAllStores(inner)
	device.LIDs = c.lid
	device.Container = c
	device.Initialized = true
}

// scannable matches both *sql.Row and *sql.Rows.
type scannable interface {
	Scan(dest ...any) error
}
