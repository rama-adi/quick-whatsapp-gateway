package mysqlstore

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/types"
)

// test helpers shared across the package's tests.

func jidPtr(s string) (*types.JID, error) {
	jid, err := types.ParseJID(s)
	if err != nil {
		return nil, err
	}
	return &jid, nil
}

func newEmptyAccount() *waAdv.ADVSignedDeviceIdentity {
	return &waAdv.ADVSignedDeviceIdentity{
		Details:             []byte{},
		AccountSignature:    make([]byte, 64),
		AccountSignatureKey: make([]byte, 32),
		DeviceSignature:     make([]byte, 64),
	}
}

func TestPutLIDMapping(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	m := newLIDMap(db)

	lid, err := types.ParseJID("555@lid")
	require.NoError(t, err)
	pn, err := types.ParseJID("628111@s.whatsapp.net")
	require.NoError(t, err)

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM wmstore_lid_map WHERE lid<>? AND pn=?`)).
		WithArgs("555", "628111").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO wmstore_lid_map (lid, pn)`)).
		WithArgs("555", "628111").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	require.NoError(t, m.PutLIDMapping(context.Background(), lid, pn))
	assert.NoError(t, mock.ExpectationsWereMet())

	// Second identical put is served from cache, no DB calls expected.
	require.NoError(t, m.PutLIDMapping(context.Background(), lid, pn))
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestGetLIDForPN(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	m := newLIDMap(db)

	pn, err := types.ParseJID("628111@s.whatsapp.net")
	require.NoError(t, err)

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT lid FROM wmstore_lid_map WHERE pn=?`)).
		WithArgs("628111").
		WillReturnRows(sqlmock.NewRows([]string{"lid"}).AddRow("555"))

	got, err := m.GetLIDForPN(context.Background(), pn)
	require.NoError(t, err)
	assert.Equal(t, "555", got.User)
	assert.Equal(t, types.HiddenUserServer, got.Server)

	// Cached: no further query expected.
	got2, err := m.GetLIDForPN(context.Background(), pn)
	require.NoError(t, err)
	assert.Equal(t, "555", got2.User)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestGetPNForLID(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	m := newLIDMap(db)

	lid, err := types.ParseJID("555@lid")
	require.NoError(t, err)

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT pn FROM wmstore_lid_map WHERE lid=?`)).
		WithArgs("555").
		WillReturnRows(sqlmock.NewRows([]string{"pn"}).AddRow("628111"))

	got, err := m.GetPNForLID(context.Background(), lid)
	require.NoError(t, err)
	assert.Equal(t, "628111", got.User)
	assert.Equal(t, types.DefaultUserServer, got.Server)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestGetLIDForPNRejectsNonPN(t *testing.T) {
	db, _, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	m := newLIDMap(db)
	bad, err := types.ParseJID("555@lid")
	require.NoError(t, err)
	_, err = m.GetLIDForPN(context.Background(), bad)
	require.Error(t, err)
}
