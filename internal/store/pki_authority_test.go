package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

func TestRotatePKIAuthorityInsertFailureRollsBack(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id FROM pki_rotation_lock").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	cols := []string{"id", "status", "certificate_pem", "certificate_fingerprint", "encrypted_private_key", "private_key_nonce", "encryption_key_id", "not_before", "not_after", "created_at", "updated_at", "active_slot"}
	// Rotation must load the active row even when its signing validity expired.
	mock.ExpectQuery("FROM pki_authorities WHERE status='active' ORDER BY").WillReturnRows(sqlmock.NewRows(cols).AddRow("ca-old", "active", "cert", []byte("fp"), []byte("key"), []byte("nonce"), "kek", 0, 50, 1, 1, 1))
	mock.ExpectExec("UPDATE pki_authorities SET status='retiring'").WithArgs(int64(100)).WillReturnResult(sqlmock.NewResult(0, 1))
	insertErr := errors.New("insert failed")
	mock.ExpectExec("INSERT INTO pki_authorities").WithArgs("ca-new", "cert", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), "kek", int64(100), int64(2000), int64(100), int64(100)).WillReturnError(insertErr)
	mock.ExpectRollback()
	next := domain.PKIAuthority{ID: "ca-new", CertificatePEM: "cert", CertificateFingerprint: []byte("fp2"), EncryptedPrivateKey: []byte("key2"), PrivateKeyNonce: []byte("nonce2"), EncryptionKeyID: "kek", NotBefore: 100, NotAfter: 2000, CreatedAt: 100, UpdatedAt: 100}
	if err := New(db).RotatePKIAuthority(context.Background(), next, 100); !errors.Is(err, insertErr) {
		t.Fatalf("got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestActiveAuthorityQueryIsLockedAndDeterministic(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	cols := []string{"id", "status", "certificate_pem", "certificate_fingerprint", "encrypted_private_key", "private_key_nonce", "encryption_key_id", "not_before", "not_after", "created_at", "updated_at", "active_slot"}
	mock.ExpectQuery("ORDER BY created_at DESC LIMIT 1 FOR UPDATE").WithArgs(int64(10)).WillReturnRows(sqlmock.NewRows(cols).AddRow("ca", "active", "cert", []byte{}, []byte{}, []byte{}, "k", 0, 20, 1, 1, sql.NullInt16{Int16: 1, Valid: true}))
	a, err := NewPKIAuthorityRepo(db).ActiveForUpdate(context.Background(), 10)
	if err != nil || a.ID != "ca" {
		t.Fatalf("authority=%+v err=%v", a, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
