package mysqlstore

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.mau.fi/whatsmeow/store"
)

func TestPutDeviceRequiresID(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	c := NewContainer(db, nil)
	dev := c.NewDevice() // ID is nil
	err = c.PutDevice(context.Background(), dev)
	require.ErrorIs(t, err, ErrDeviceIDMustBeSet)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestPutDeviceUpsert(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	c := NewContainer(db, nil)

	dev := c.NewDevice()
	jid, err := jidPtr("628111.0:1@s.whatsapp.net")
	require.NoError(t, err)
	dev.ID = jid

	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO wmstore_device`)).
		WithArgs(
			dev.ID,                        // jid
			dev.LID,                       // lid
			dev.RegistrationID,            // registration_id
			dev.NoiseKey.Priv[:],          // noise_key
			dev.IdentityKey.Priv[:],       // identity_key
			dev.SignedPreKey.Priv[:],      // signed_pre_key
			dev.SignedPreKey.KeyID,        // signed_pre_key_id
			dev.SignedPreKey.Signature[:], // signed_pre_key_sig
			dev.AdvSecretKey,              // adv_key
			sqlmock.AnyArg(),              // adv_details (nil-able proto bytes)
			sqlmock.AnyArg(),              // adv_account_sig
			sqlmock.AnyArg(),              // adv_account_sig_key
			sqlmock.AnyArg(),              // adv_device_sig
			dev.Platform,                  // platform
			dev.BusinessName,              // business_name
			dev.PushName,                  // push_name
			nil,                           // facebook_uuid (Nil uuid -> nil)
			dev.LIDMigrationTimestamp,     // lid_migration_ts
		).
		WillReturnResult(sqlmock.NewResult(1, 1))

	// PutDevice needs a non-nil Account for the signature fields.
	dev.Account = newEmptyAccount()
	require.NoError(t, c.PutDevice(context.Background(), dev))
	assert.NoError(t, mock.ExpectationsWereMet())

	// After PutDevice the per-device + global stores must be wired.
	assert.True(t, dev.Initialized)
	assert.NotNil(t, dev.Identities)
	assert.NotNil(t, dev.LIDs)
	var _ store.DeviceContainer = c
}

func TestDeleteDevice(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	c := NewContainer(db, nil)

	dev := c.NewDevice()
	jid, err := jidPtr("628111.0:1@s.whatsapp.net")
	require.NoError(t, err)
	dev.ID = jid

	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM wmstore_device WHERE jid=?`)).
		WithArgs(dev.ID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, c.DeleteDevice(context.Background(), dev))
	assert.NoError(t, mock.ExpectationsWereMet())
}
