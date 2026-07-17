// Package dbmigrate owns execution of the API-managed WA application schema.
package dbmigrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/golang-migrate/migrate/v4"
	migratemysql "github.com/golang-migrate/migrate/v4/database/mysql"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/ramaadi/quick-whatsapp-gateway/migrations"
)

type Direction string

const (
	Up   Direction = "up"
	Down Direction = "down"
)

func ParseDirection(args []string) (Direction, error) {
	if len(args) != 1 {
		return "", fmt.Errorf("usage: migrate up|down")
	}
	direction := Direction(args[0])
	if direction != Up && direction != Down {
		return "", fmt.Errorf("unknown migration direction %q (want up|down)", args[0])
	}
	return direction, nil
}

type executor interface {
	Up() error
	Steps(int) error
}

type lifecycle interface {
	executor
	Close() (source error, database error)
}

func Apply(direction Direction, runner executor) error {
	var err error
	switch direction {
	case Up:
		err = runner.Up()
	case Down:
		err = runner.Steps(-1)
	default:
		return fmt.Errorf("unknown migration direction %q (want up|down)", direction)
	}
	if errors.Is(err, migrate.ErrNoChange) {
		return nil
	}
	return err
}

func Run(dsn string, direction Direction) error {
	db, err := openMySQL(dsn)
	if err != nil {
		return err
	}
	constructed := false
	defer func() {
		if !constructed {
			_ = db.Close()
		}
	}()
	return runConstructed(direction, func() (lifecycle, error) {
		runner, err := newLifecycle(db)
		if err == nil {
			constructed = true
		}
		return runner, err
	})
}

func newLifecycle(db *sql.DB) (lifecycle, error) {
	source, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return nil, fmt.Errorf("open migration source: %w", err)
	}
	driver, err := migratemysql.WithInstance(db, &migratemysql.Config{})
	if err != nil {
		return nil, errors.Join(fmt.Errorf("migration driver: %w", err), source.Close())
	}
	runner, err := migrate.NewWithInstance("iofs", source, "mysql", driver)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("migration instance: %w", err), source.Close(), driver.Close())
	}
	return runner, nil
}

func runConstructed(direction Direction, factory func() (lifecycle, error)) error {
	runner, err := factory()
	if err != nil {
		return err
	}
	applyErr := Apply(direction, runner)
	sourceErr, databaseErr := runner.Close()
	return errors.Join(applyErr, sourceErr, databaseErr)
}

func openMySQL(dsn string) (*sql.DB, error) {
	if dsn == "" {
		return nil, fmt.Errorf("MYSQL_DSN is required")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse MYSQL_DSN: %w", err)
	}
	cfg.MultiStatements = true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("open migration mysql: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping migration mysql: %w", err)
	}
	return db, nil
}
