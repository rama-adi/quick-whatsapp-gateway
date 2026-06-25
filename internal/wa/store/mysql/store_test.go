package mysqlstore

import (
	"context"
	"database/sql"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

const testJID = "628111@s.whatsapp.net"

func newTestStore(t *testing.T) (*mysqlStore, sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	jid, err := types.ParseJID(testJID)
	require.NoError(t, err)
	s := newMysqlStore(db, nil, jid)
	return s, mock, func() {
		assert.NoError(t, mock.ExpectationsWereMet())
		_ = db.Close()
	}
}

func TestPutIdentityUpsert(t *testing.T) {
	s, mock, done := newTestStore(t)
	defer done()

	var key [32]byte
	for i := range key {
		key[i] = byte(i)
	}
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO wmstore_identity_keys`)).
		WithArgs(testJID, "their:1", key[:]).
		WillReturnResult(sqlmock.NewResult(1, 1))

	require.NoError(t, s.PutIdentity(context.Background(), "their:1", key))
}

func TestIsTrustedIdentity(t *testing.T) {
	var key [32]byte
	key[0] = 0xAA

	tests := []struct {
		name    string
		stored  []byte
		noRows  bool
		want    bool
		wantErr bool
	}{
		{name: "unknown is trusted", noRows: true, want: true},
		{name: "matching is trusted", stored: key[:], want: true},
		{name: "mismatch not trusted", stored: make([]byte, 32), want: false},
		{name: "bad length errors", stored: []byte{1, 2, 3}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, mock, done := newTestStore(t)
			defer done()
			q := mock.ExpectQuery(regexp.QuoteMeta(`SELECT identity FROM wmstore_identity_keys`)).
				WithArgs(testJID, "addr")
			if tc.noRows {
				q.WillReturnError(sql.ErrNoRows)
			} else {
				q.WillReturnRows(sqlmock.NewRows([]string{"identity"}).AddRow(tc.stored))
			}
			got, err := s.IsTrustedIdentity(context.Background(), "addr", key)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestGetSession(t *testing.T) {
	t.Run("found", func(t *testing.T) {
		s, mock, done := newTestStore(t)
		defer done()
		mock.ExpectQuery(regexp.QuoteMeta(`SELECT session FROM wmstore_sessions`)).
			WithArgs(testJID, "addr").
			WillReturnRows(sqlmock.NewRows([]string{"session"}).AddRow([]byte("blob")))
		got, err := s.GetSession(context.Background(), "addr")
		require.NoError(t, err)
		assert.Equal(t, []byte("blob"), got)
	})
	t.Run("missing returns nil no error", func(t *testing.T) {
		s, mock, done := newTestStore(t)
		defer done()
		mock.ExpectQuery(regexp.QuoteMeta(`SELECT session FROM wmstore_sessions`)).
			WithArgs(testJID, "addr").
			WillReturnError(sql.ErrNoRows)
		got, err := s.GetSession(context.Background(), "addr")
		require.NoError(t, err)
		assert.Nil(t, got)
	})
}

func TestPutSessionUpsert(t *testing.T) {
	s, mock, done := newTestStore(t)
	defer done()
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO wmstore_sessions`)).
		WithArgs(testJID, "addr", []byte("sess")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, s.PutSession(context.Background(), "addr", []byte("sess")))
}

func TestGetPreKey(t *testing.T) {
	s, mock, done := newTestStore(t)
	defer done()
	var priv [32]byte
	priv[0] = 9
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT key_id, `+"`key`"+` FROM wmstore_pre_keys`)).
		WithArgs(testJID, uint32(7)).
		WillReturnRows(sqlmock.NewRows([]string{"key_id", "key"}).AddRow(uint32(7), priv[:]))
	pk, err := s.GetPreKey(context.Background(), 7)
	require.NoError(t, err)
	require.NotNil(t, pk)
	assert.Equal(t, uint32(7), pk.KeyID)
	assert.Equal(t, priv, *pk.Priv)
}

func TestGetPreKeyMissing(t *testing.T) {
	s, mock, done := newTestStore(t)
	defer done()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT key_id, `+"`key`"+` FROM wmstore_pre_keys`)).
		WithArgs(testJID, uint32(7)).
		WillReturnError(sql.ErrNoRows)
	pk, err := s.GetPreKey(context.Background(), 7)
	require.NoError(t, err)
	assert.Nil(t, pk)
}

func TestRemovePreKey(t *testing.T) {
	s, mock, done := newTestStore(t)
	defer done()
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM wmstore_pre_keys`)).
		WithArgs(testJID, uint32(3)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, s.RemovePreKey(context.Background(), 3))
}

func TestMarkPreKeysAsUploaded(t *testing.T) {
	s, mock, done := newTestStore(t)
	defer done()
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE wmstore_pre_keys SET uploaded=true`)).
		WithArgs(testJID, uint32(42)).
		WillReturnResult(sqlmock.NewResult(0, 5))
	require.NoError(t, s.MarkPreKeysAsUploaded(context.Background(), 42))
}

func TestUploadedPreKeyCount(t *testing.T) {
	s, mock, done := newTestStore(t)
	defer done()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*) FROM wmstore_pre_keys`)).
		WithArgs(testJID).
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(11))
	n, err := s.UploadedPreKeyCount(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 11, n)
}

func TestGetOrGenPreKeysGeneratesMissing(t *testing.T) {
	s, mock, done := newTestStore(t)
	defer done()
	// One existing unuploaded key, request 3 -> generate 2 more.
	var existing [32]byte
	existing[0] = 1
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT key_id, `+"`key`"+` FROM wmstore_pre_keys WHERE jid=? AND uploaded=false`)).
		WithArgs(testJID, uint32(3)).
		WillReturnRows(sqlmock.NewRows([]string{"key_id", "key"}).AddRow(uint32(1), existing[:]))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT MAX(key_id) FROM wmstore_pre_keys`)).
		WithArgs(testJID).
		WillReturnRows(sqlmock.NewRows([]string{"m"}).AddRow(int64(1)))
	// Two inserts for ids 2 and 3 (markUploaded=false).
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO wmstore_pre_keys`)).
		WithArgs(testJID, uint32(2), sqlmock.AnyArg(), false).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO wmstore_pre_keys`)).
		WithArgs(testJID, uint32(3), sqlmock.AnyArg(), false).
		WillReturnResult(sqlmock.NewResult(0, 1))

	out, err := s.GetOrGenPreKeys(context.Background(), 3)
	require.NoError(t, err)
	require.Len(t, out, 3)
	assert.Equal(t, uint32(1), out[0].KeyID)
	assert.Equal(t, uint32(2), out[1].KeyID)
	assert.Equal(t, uint32(3), out[2].KeyID)
}

func TestAppStateVersionRoundTrip(t *testing.T) {
	s, mock, done := newTestStore(t)
	defer done()

	var hash [128]byte
	for i := range hash {
		hash[i] = byte(i)
	}
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO wmstore_app_state_version`)).
		WithArgs(testJID, "critical_block", uint64(5), hash[:]).
		WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, s.PutAppStateVersion(context.Background(), "critical_block", 5, hash))

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT version, hash FROM wmstore_app_state_version`)).
		WithArgs(testJID, "critical_block").
		WillReturnRows(sqlmock.NewRows([]string{"version", "hash"}).AddRow(uint64(5), hash[:]))
	gotVer, gotHash, err := s.GetAppStateVersion(context.Background(), "critical_block")
	require.NoError(t, err)
	assert.Equal(t, uint64(5), gotVer)
	assert.Equal(t, hash, gotHash)
}

func TestGetAppStateVersionMissingIsZero(t *testing.T) {
	s, mock, done := newTestStore(t)
	defer done()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT version, hash FROM wmstore_app_state_version`)).
		WithArgs(testJID, "name").
		WillReturnError(sql.ErrNoRows)
	ver, hash, err := s.GetAppStateVersion(context.Background(), "name")
	require.NoError(t, err)
	assert.Equal(t, uint64(0), ver)
	assert.Equal(t, [128]byte{}, hash)
}

func TestPutGetSenderKey(t *testing.T) {
	s, mock, done := newTestStore(t)
	defer done()
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO wmstore_sender_keys`)).
		WithArgs(testJID, "group", "user", []byte("sk")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, s.PutSenderKey(context.Background(), "group", "user", []byte("sk")))

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT sender_key FROM wmstore_sender_keys`)).
		WithArgs(testJID, "group", "user").
		WillReturnRows(sqlmock.NewRows([]string{"sender_key"}).AddRow([]byte("sk")))
	got, err := s.GetSenderKey(context.Background(), "group", "user")
	require.NoError(t, err)
	assert.Equal(t, []byte("sk"), got)
}

func TestPutManySessionsUsesTxn(t *testing.T) {
	s, mock, done := newTestStore(t)
	defer done()
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO wmstore_sessions`)).
		WithArgs(testJID, "a", []byte("1")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	require.NoError(t, s.PutManySessions(context.Background(), map[string][]byte{"a": []byte("1")}))
}

func TestPutManySessionsRollsBackOnError(t *testing.T) {
	s, mock, done := newTestStore(t)
	defer done()
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO wmstore_sessions`)).
		WithArgs(testJID, "a", []byte("1")).
		WillReturnError(assertErr)
	mock.ExpectRollback()
	err := s.PutManySessions(context.Background(), map[string][]byte{"a": []byte("1")})
	require.Error(t, err)
}

func TestGetChatSettingsMutedForever(t *testing.T) {
	s, mock, done := newTestStore(t)
	defer done()
	chat, err := types.ParseJID("123@g.us")
	require.NoError(t, err)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT muted_until, pinned, archived FROM wmstore_chat_settings`)).
		WithArgs(testJID, chat).
		WillReturnRows(sqlmock.NewRows([]string{"muted_until", "pinned", "archived"}).AddRow(int64(-1), false, true))
	got, err := s.GetChatSettings(context.Background(), chat)
	require.NoError(t, err)
	assert.True(t, got.Found)
	assert.True(t, got.Archived)
	assert.Equal(t, store.MutedForever, got.MutedUntil)
}

func TestNCTSaltRoundTrip(t *testing.T) {
	s, mock, done := newTestStore(t)
	defer done()
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO wmstore_nct_salt`)).
		WithArgs(testJID, []byte("salt")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, s.PutNCTSalt(context.Background(), []byte("salt")))

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT salt FROM wmstore_nct_salt`)).
		WithArgs(testJID).
		WillReturnRows(sqlmock.NewRows([]string{"salt"}).AddRow([]byte("salt")))
	got, err := s.GetNCTSalt(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []byte("salt"), got)
}

var assertErr = sql.ErrConnDone
