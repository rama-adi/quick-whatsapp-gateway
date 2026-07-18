package store

import (
	"context"
	"database/sql"
	"errors"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"testing"
)

func authorityCols() []string {
	return []string{"id", "kind", "parent_authority_id", "parent_kind", "status", "certificate_pem", "certificate_fingerprint", "encrypted_private_key", "private_key_nonce", "encryption_key_id", "not_before", "not_after", "created_at", "updated_at", "active_kind"}
}
func TestRotatePKIAuthorityInsertFailureRollsBack(t *testing.T) {
	db, m, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = db.Close() }()
	m.ExpectBegin()
	m.ExpectQuery("SELECT id FROM pki_rotation_lock").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	m.ExpectQuery("FROM pki_authorities WHERE kind=.+status='active' ORDER BY").WithArgs("root").WillReturnRows(sqlmock.NewRows(authorityCols()).AddRow("old", "root", nil, nil, "active", "c", []byte{}, []byte{}, []byte{}, "k", 0, 50, 1, 1, "root"))
	m.ExpectExec("UPDATE pki_authorities SET status='retiring'").WithArgs(int64(100), "root").WillReturnResult(sqlmock.NewResult(0, 1))
	boom := errors.New("insert failed")
	m.ExpectExec("INSERT INTO pki_authorities").WithArgs("new", "root", nil, sqlmock.AnyArg(), "c", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), "k", int64(100), int64(200), int64(100), int64(100)).WillReturnError(boom)
	m.ExpectRollback()
	next := domain.PKIAuthority{ID: "new", Kind: "root", CertificatePEM: "c", EncryptionKeyID: "k", NotBefore: 100, NotAfter: 200, CreatedAt: 100, UpdatedAt: 100}
	if e := New(db).RotatePKIAuthority(context.Background(), next, 100); !errors.Is(e, boom) {
		t.Fatal(e)
	}
	if e := m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}
func TestActiveAuthorityQueryIsLockedAndDeterministic(t *testing.T) {
	db, m, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = db.Close() }()
	m.ExpectQuery("ORDER BY created_at DESC LIMIT 1 FOR UPDATE").WithArgs("intermediate", int64(10)).WillReturnRows(sqlmock.NewRows(authorityCols()).AddRow("ca", "intermediate", "root", "root", "active", "c", []byte{}, []byte{}, []byte{}, "k", 0, 20, 1, 1, sql.NullString{String: "intermediate", Valid: true}))
	a, e := NewPKIAuthorityRepo(db).ActiveForUpdate(context.Background(), "intermediate", 10)
	if e != nil || a.ID != "ca" {
		t.Fatalf("%+v %v", a, e)
	}
	if e := m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}
func TestRotateIntermediateReparentsToActiveRoot(t *testing.T) {
	db, m, e := sqlmock.New()
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = db.Close() }()
	m.ExpectBegin()
	m.ExpectQuery("SELECT id FROM pki_rotation_lock").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	m.ExpectQuery("WHERE kind=.+status='active' ORDER BY").WithArgs("intermediate").WillReturnRows(sqlmock.NewRows(authorityCols()).AddRow("intA", "intermediate", "rootA", "root", "active", "c", []byte{}, []byte{}, []byte{}, "k", 0, 200, 1, 1, "intermediate"))
	m.ExpectQuery("WHERE kind=.+status='active' AND not_after").WithArgs("root", int64(100)).WillReturnRows(sqlmock.NewRows(authorityCols()).AddRow("rootB", "root", nil, nil, "active", "c", []byte{}, []byte{}, []byte{}, "k", 0, 200, 1, 1, "root"))
	m.ExpectExec("UPDATE pki_authorities SET status='retiring'").WithArgs(int64(100), "intermediate").WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectExec("INSERT INTO pki_authorities").WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectCommit()
	parent := "rootB"
	next := domain.PKIAuthority{ID: "intB", Kind: "intermediate", ParentAuthorityID: &parent, CertificatePEM: "c", EncryptionKeyID: "k", NotBefore: 100, NotAfter: 200, CreatedAt: 100, UpdatedAt: 100}
	if e := New(db).RotatePKIAuthority(context.Background(), next, 100); e != nil {
		t.Fatal(e)
	}
	if e := m.ExpectationsWereMet(); e != nil {
		t.Fatal(e)
	}
}
