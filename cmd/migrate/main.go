// Command migrate applies or rolls back the API-owned WA application schema.
package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/dbmigrate"
)

func main() {
	if err := run(os.Args[1:], os.Getenv, dbmigrate.Run); err != nil {
		slog.Error("migration failed", "err", err)
		os.Exit(1)
	}
}

func run(args []string, getenv func(string) string, execute func(string, dbmigrate.Direction) error) error {
	direction, err := dbmigrate.ParseDirection(args)
	if err != nil {
		return err
	}
	if err := execute(getenv("MYSQL_DSN"), direction); err != nil {
		return fmt.Errorf("migrate %s: %w", direction, err)
	}
	return nil
}
