// Package migrations embeds the SQL schema migrations so the gateway binary can
// run them on boot (and via the `server migrate up|down` subcommand) without
// shipping the .sql files alongside it. The files are applied by golang-migrate
// over the iofs source.
package migrations

import "embed"

// FS holds every *.sql migration in lexical (version) order:
// 0001_init.{up,down}.sql, 0002_wmstore.{up,down}.sql.
//
//go:embed *.sql
var FS embed.FS
