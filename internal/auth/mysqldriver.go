package auth

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"regexp"
	"sync"

	"github.com/go-sql-driver/mysql"
)

// Authula v1.12.0's migration recorder (github.com/Authula/authula/migrations
// .Migrator) records every applied migration with
//
//	tx.NewInsert().Model(entry).ModelTableExpr("? AS schema_migration", ...)
//
// bun v1.2.18's mysqldialect renders that model-table expression verbatim, so
// the statement comes out as
//
//	INSERT INTO `auth_schema_migrations` AS schema_migration (...) VALUES (...)
//
// MySQL has NO table-alias syntax on INSERT (the 8.0.19 row alias goes *after*
// VALUES), so the server rejects it with error 1064 and Authula.New panics
// before the gateway can boot. The same `? AS schema_migration` expression is
// reused for SELECT/DELETE, where `tbl AS alias` IS valid MySQL and is needed,
// so we must fix ONLY the INSERT form.
//
// We cannot edit the vendored module, and there is no newer Authula/bun release
// that fixes it. The clean, narrowly-scoped fix is a thin driver shim: wrap the
// go-sql-driver/mysql driver and, just before the statement reaches MySQL,
// strip the bogus alias from this one INSERT shape. Everything else passes
// through untouched. We then hand Authula a *bun.DB opened on this shim via
// AuthConfig.DB (recon §4), so its migrator records succeed.

// insertAliasRe matches the broken Authula migrator INSERT and captures the
// table-name token so we can drop the ` AS <alias>` that follows it. It is
// deliberately specific: an INSERT INTO whose table name is immediately
// followed by `AS <ident>` and then the column list `(`. Valid MySQL never
// produces this shape, so the rewrite is safe for any query, but anchoring on
// INSERT keeps SELECT/DELETE aliases (which are legal) untouched.
var insertAliasRe = regexp.MustCompile(`(?is)(INSERT\s+(?:IGNORE\s+)?INTO\s+` + "`" + `?[\w.]+` + "`" + `?)\s+AS\s+\w+\s*\(`)

// bareKeyColumnRe matches a `key` column definition that Authula's MySQL
// migrations (e.g. access-control's access_control_permissions table) emit
// UNQUOTED. `KEY` is a reserved word in MySQL, so the CREATE TABLE fails with a
// 1064 syntax error. We backtick it. The match is anchored on a column-list
// delimiter (`(` or `,`) immediately before `key`, followed by whitespace and a
// type token, so it never touches `PRIMARY KEY` / `UNIQUE KEY` / `FOREIGN KEY`
// (where KEY is preceded by a keyword, not a delimiter) or an already-quoted
// “key“ column.
var bareKeyColumnRe = regexp.MustCompile(`(?is)([(,]\s*)key(\s+(?:VARCHAR|CHAR|TEXT|LONGTEXT|MEDIUMTEXT|TINYTEXT|INT|INTEGER|BIGINT|VARBINARY|BLOB)\b)`)

// binary16Re matches the BINARY(16) column type Authula's MySQL plugin
// migrations (access-control, admin, totp) use for every id / *_user_id /
// role_id / permission_id / session_id column. Their Go models store those ids
// as 36-char UUID *strings* (util.GenerateUUID() -> uuid.String()), so any
// INSERT overflows BINARY(16) with MySQL error 1406 "Data too long for column
// 'id'". The core tables (users/accounts/sessions) already use VARCHAR(255) for
// their string ids, and the plugin FKs reference users(id), so rewriting
// BINARY(16) -> VARCHAR(255) is both correct (holds the UUID string) and
// consistent (FK column type matches the referenced VARCHAR(255) PK). It only
// ever appears in DDL, so the rewrite cannot touch DML.
var binary16Re = regexp.MustCompile(`(?i)BINARY\(16\)`)

// fixAuthulaSQL rewrites the three classes of malformed SQL that Authula
// v1.12.0's MySQL migrations produce (see the package-level comment above):
//
//  1. INSERT ... AS alias ( ...  -> INSERT ... ( ...   (illegal table alias)
//  2. a bare reserved-word column `key` -> “key“  (missing quoting)
//  3. id columns typed BINARY(16) -> VARCHAR(255)  (can't hold UUID strings)
//
// Queries that match none of the patterns are returned byte-for-byte unchanged.
func fixAuthulaSQL(query string) string {
	query = insertAliasRe.ReplaceAllString(query, "$1 (")
	query = bareKeyColumnRe.ReplaceAllString(query, "${1}`key`${2}")
	query = binary16Re.ReplaceAllString(query, "VARCHAR(255)")
	return query
}

const aliasFixDriverName = "mysql-authula-aliasfix"

var registerOnce sync.Once

// registerAliasFixDriver registers the shim driver exactly once. It is safe to
// call repeatedly (e.g. across tests).
func registerAliasFixDriver() {
	registerOnce.Do(func() {
		sql.Register(aliasFixDriverName, aliasFixDriver{base: mysql.MySQLDriver{}})
	})
}

// openAuthulaMySQL opens a *sql.DB on the alias-fixing shim driver for the given
// go-sql-driver DSN.
func openAuthulaMySQL(dsn string) (*sql.DB, error) {
	registerAliasFixDriver()
	return sql.Open(aliasFixDriverName, dsn)
}

// --- driver shim -----------------------------------------------------------
//
// We wrap the connector so that the *sql.DB pool, context cancellation, and the
// mysql driver's own behavior are all preserved; only the SQL text of prepared
// statements and direct Exec/Query calls is rewritten on the way through.

type aliasFixDriver struct{ base mysql.MySQLDriver }

func (d aliasFixDriver) Open(dsn string) (driver.Conn, error) {
	c, err := d.base.Open(dsn)
	if err != nil {
		return nil, err
	}
	return aliasFixConn{c}, nil
}

// OpenConnector lets database/sql reuse one connector (and its DSN parsing)
// across the pool, which the mysql driver supports.
func (d aliasFixDriver) OpenConnector(dsn string) (driver.Connector, error) {
	c, err := d.base.OpenConnector(dsn)
	if err != nil {
		return nil, err
	}
	return aliasFixConnector{c, d}, nil
}

type aliasFixConnector struct {
	base   driver.Connector
	driver aliasFixDriver
}

func (c aliasFixConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return aliasFixConn{conn}, nil
}

func (c aliasFixConnector) Driver() driver.Driver { return c.driver }

// aliasFixConn forwards everything to the underlying mysql conn but rewrites the
// query text on Prepare/Exec/Query. The mysql driver's conn implements the
// context-aware and transaction interfaces; we assert and delegate to them.
type aliasFixConn struct{ base driver.Conn }

func (c aliasFixConn) Prepare(query string) (driver.Stmt, error) {
	return c.base.Prepare(fixAuthulaSQL(query))
}

func (c aliasFixConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if p, ok := c.base.(driver.ConnPrepareContext); ok {
		return p.PrepareContext(ctx, fixAuthulaSQL(query))
	}
	return c.base.Prepare(fixAuthulaSQL(query))
}

func (c aliasFixConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if e, ok := c.base.(driver.ExecerContext); ok {
		return e.ExecContext(ctx, fixAuthulaSQL(query), args)
	}
	return nil, driver.ErrSkip
}

func (c aliasFixConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if q, ok := c.base.(driver.QueryerContext); ok {
		return q.QueryContext(ctx, fixAuthulaSQL(query), args)
	}
	return nil, driver.ErrSkip
}

func (c aliasFixConn) Begin() (driver.Tx, error) { return c.base.Begin() } //nolint:staticcheck // forwarding

func (c aliasFixConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if b, ok := c.base.(driver.ConnBeginTx); ok {
		return b.BeginTx(ctx, opts)
	}
	return c.base.Begin() //nolint:staticcheck // forwarding
}

func (c aliasFixConn) Close() error { return c.base.Close() }

// Ping / ResetSession / CheckNamedValue / IsValid are delegated when supported
// so the shim is transparent to database/sql's connection management.

func (c aliasFixConn) Ping(ctx context.Context) error {
	if p, ok := c.base.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

func (c aliasFixConn) ResetSession(ctx context.Context) error {
	if r, ok := c.base.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

func (c aliasFixConn) CheckNamedValue(nv *driver.NamedValue) error {
	if cv, ok := c.base.(driver.NamedValueChecker); ok {
		return cv.CheckNamedValue(nv)
	}
	return driver.ErrSkip
}

func (c aliasFixConn) IsValid() bool {
	if v, ok := c.base.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}
