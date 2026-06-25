package store

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
)

// newMock returns a sqlmock-backed *sql.DB (which satisfies dbExecQuerier) using
// regexp query matching, so tests assert on meaningful SQL fragments rather than
// exact whitespace.
func newMock(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, mock
}

func strptr(s string) *string { return &s }
func i64ptr(i int64) *int64   { return &i }
func intptr(i int) *int       { return &i }

// noRows is the sentinel sqlmock returns to simulate sql.ErrNoRows.
func noRows() error { return sql.ErrNoRows }

// asAPIError unwraps err into a *domain.APIError target.
func asAPIError(err error, target **domain.APIError) bool {
	return errors.As(err, target)
}

// assertNotFound asserts err is a domain not_found APIError.
func assertNotFound(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var apiErr *domain.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != domain.CodeNotFound {
		t.Fatalf("want not_found APIError, got %v", err)
	}
}
