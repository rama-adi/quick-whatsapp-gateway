// Package migrations embeds the API-owned WA application schema for automatic
// API startup migration and the dedicated `cmd/migrate up|down` command. The
// gateway runtime does not import or execute this package.
package migrations

import "embed"

// FS holds every *.sql migration in lexical version order. The normalized
// gateway lifecycle/enrollment/PKI/audit foundation lives in the clean 0001
// baseline; the obsolete 0004 lifecycle ALTER was folded into it. The whatsmeow keystore
// lives in gateway-local SQLite and is auto-migrated by whatsmeow's sqlstore, so
// there are no wmstore_* migrations here.
//
//go:embed *.sql
var FS embed.FS
