package main

import (
	"errors"
	"testing"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/dbmigrate"
)

func TestRunUsageAndExecution(t *testing.T) {
	if err := run(nil, func(string) string { return "" }, nil); err == nil || err.Error() != "usage: migrate up|down" {
		t.Fatalf("usage error = %v", err)
	}
	want := errors.New("db unavailable")
	err := run([]string{"down"}, func(key string) string {
		if key != "MYSQL_DSN" {
			t.Fatalf("environment key = %q", key)
		}
		return "dsn"
	}, func(dsn string, direction dbmigrate.Direction) error {
		if dsn != "dsn" || direction != dbmigrate.Down {
			t.Fatalf("execute = (%q, %q)", dsn, direction)
		}
		return want
	})
	if !errors.Is(err, want) || err.Error() != "migrate down: db unavailable" {
		t.Fatalf("execution error = %v", err)
	}
}

func TestRunSuccessDirections(t *testing.T) {
	for _, direction := range []dbmigrate.Direction{dbmigrate.Up, dbmigrate.Down} {
		t.Run(string(direction), func(t *testing.T) {
			called := false
			err := run([]string{string(direction)}, func(key string) string {
				if key != "MYSQL_DSN" {
					t.Fatalf("environment key = %q", key)
				}
				return "dsn"
			}, func(dsn string, got dbmigrate.Direction) error {
				called = true
				if dsn != "dsn" || got != direction {
					t.Fatalf("execute = (%q, %q), want (%q, %q)", dsn, got, "dsn", direction)
				}
				return nil
			})
			if err != nil {
				t.Fatalf("run(%q): %v", direction, err)
			}
			if !called {
				t.Fatal("executor was not called")
			}
		})
	}
}
