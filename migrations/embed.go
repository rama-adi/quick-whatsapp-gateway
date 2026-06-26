// Package migrations embeds the SQL schema migrations so the gateway binary can
// run them on boot (and via the `server migrate up|down` subcommand) without
// shipping the .sql files alongside it. The files are applied by golang-migrate
// over the iofs source.
package migrations

import "embed"

// FS holds every *.sql migration in lexical (version) order:
// 0001_init.{up,down}.sql (the v2 WA app-data schema). The whatsmeow keystore
// lives in gateway-local SQLite and is auto-migrated by whatsmeow's sqlstore, so
// there are no wmstore_* migrations here.
//
//go:embed *.sql
var FS embed.FS
