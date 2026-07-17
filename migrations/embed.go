// Package migrations embeds the API-owned WA application schema for automatic
// API startup migration and the dedicated `cmd/migrate up|down` command. The
// gateway runtime does not import or execute this package.
package migrations

import "embed"

// FS holds every *.sql migration in lexical (version) order:
// 0001_init.{up,down}.sql (the v2 WA app-data schema). The whatsmeow keystore
// lives in gateway-local SQLite and is auto-migrated by whatsmeow's sqlstore, so
// there are no wmstore_* migrations here.
//
//go:embed *.sql
var FS embed.FS
