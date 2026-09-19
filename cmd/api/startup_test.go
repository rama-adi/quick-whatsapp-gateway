package main

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/dbmigrate"
)

func TestPrepareAPIDatabaseMigratesBeforeOpen(t *testing.T) {
	order := []string{}
	_, err := prepareAPIDatabase("dsn", func(dsn string, direction dbmigrate.Direction) error {
		order = append(order, "migrate")
		if dsn != "dsn" || direction != dbmigrate.Up {
			t.Fatalf("migration = (%q, %q)", dsn, direction)
		}
		return nil
	}, func(dsn string) (*sql.DB, error) {
		order = append(order, "open")
		return nil, nil
	})
	if err != nil || len(order) != 2 || order[0] != "migrate" || order[1] != "open" {
		t.Fatalf("order = %v, err = %v", order, err)
	}
}

func TestPrepareAPIDatabaseStopsBeforeOpenOnMigrationFailure(t *testing.T) {
	want := errors.New("migration failed")
	opened := false
	_, err := prepareAPIDatabase("dsn", func(string, dbmigrate.Direction) error { return want }, func(string) (*sql.DB, error) {
		opened = true
		return nil, nil
	})
	if !errors.Is(err, want) || err.Error() != "run API schema migrations: migration failed" || opened {
		t.Fatalf("err = %v, opened = %v", err, opened)
	}
}
