// Package internal pins the third-party dependencies the gateway will use as
// later milestones land. Each library is blank-imported here so it stays in
// go.mod/go.sum and is ready for the package that adopts it, even before any
// real code references it. Remove an entry once a package imports it directly.
//
// This file intentionally contains no logic; it is a scaffolding placeholder.
package internal

import (
	_ "github.com/Authula/authula"                       // embedded auth library (M1)
	_ "github.com/Authula/authula/config"                // authula config builder
	_ "github.com/Authula/authula/plugins/access-control" // RBAC
	_ "github.com/Authula/authula/plugins/admin"          // admin/ban/impersonation
	_ "github.com/Authula/authula/plugins/csrf"           // CSRF
	_ "github.com/Authula/authula/plugins/email-password" // login + registration
	_ "github.com/Authula/authula/plugins/rate-limit"     // auth rate limiting
	_ "github.com/Authula/authula/plugins/secondary-storage" // redis secondary storage
	_ "github.com/Authula/authula/plugins/session"        // cookie sessions
	_ "github.com/Authula/authula/plugins/totp"           // optional 2FA
	_ "github.com/go-sql-driver/mysql"                 // app-data + MySQL keystore driver
	_ "github.com/golang-migrate/migrate/v4"           // schema migrations
	_ "github.com/hibiken/asynq"                       // async outbox + webhook jobs
	_ "github.com/oklog/ulid/v2"                       // ULID ids for sessions/events/outbox
	_ "github.com/prometheus/client_golang/prometheus" // /metrics
	_ "github.com/redis/go-redis/v9"                   // queue/cache/pubsub/limits
	_ "go.mau.fi/whatsmeow"                            // WhatsApp client
	_ "go.mau.fi/whatsmeow/store/sqlstore"             // sqlite keystore fallback
	_ "golang.org/x/crypto/argon2"                     // API-key hashing
	_ "google.golang.org/protobuf/proto"               // whatsmeow message protobufs
	_ "modernc.org/sqlite"                             // pure-Go SQLite keystore driver
)
