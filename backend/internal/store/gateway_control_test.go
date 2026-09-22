package store

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestValidateAuditMetadataRecursivelyRejectsSecrets(t *testing.T) {
	for _, key := range []string{"Authorization", "private-key", "csr_pem", "certificate", "access.token"} {
		if err := validateAuditMetadata(map[string]any{"nested": []any{map[string]any{key: "redacted?"}}}); err == nil {
			t.Errorf("key %q was accepted", key)
		}
	}
	if err := validateAuditMetadata(map[string]any{"gateway_id": "gw", "certificate_fingerprint": "sha256"}); err != nil {
		t.Fatalf("safe identifiers rejected: %v", err)
	}
}

func TestInTxCommitAndRollback(t *testing.T) {
	for _, tc := range []struct {
		name     string
		callback error
		commit   bool
	}{
		{name: "commit", commit: true}, {name: "rollback", callback: errors.New("stop")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			mock.ExpectBegin()
			if tc.commit {
				mock.ExpectCommit()
			} else {
				mock.ExpectRollback()
			}
			err = InTx(context.Background(), db, func(s *Store) error {
				if s.AuditEvents == nil {
					t.Fatal("transaction store incomplete")
				}
				return tc.callback
			})
			if !errors.Is(err, tc.callback) {
				t.Fatalf("got %v want %v", err, tc.callback)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestEnrollmentOwnershipCASReturnsFalseForStaleWorker(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectExec("UPDATE gateway_enrollment_tokens").
		WithArgs(int64(200), int64(200), "tok_1", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 0))
	ok, err := newEnrollmentTokenRepo(db).finalize(context.Background(), "tok_1", []byte("0123456789abcdef"), make([]byte, 32), 200)
	if err != nil || ok {
		t.Fatalf("stale finalize: ok=%v err=%v", ok, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
