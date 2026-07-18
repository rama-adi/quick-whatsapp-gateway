// Package store holds the MySQL app-data repositories (§5 of the masterplan):
// one repo type per aggregate, each returning internal/domain types, built on
// plain database/sql with no ORM. Upserts use MySQL's ON DUPLICATE KEY UPDATE,
// queries use ? placeholders, timestamps are epoch-ms BIGINT, and list endpoints
// use opaque cursor pagination over the surrogate id column.
//
// This package owns the concrete repo implementations; the composition root
// wires them into the services and the WhatsApp engine.
package store

import (
	"context"
	"database/sql"

	"github.com/ramaadi/quick-whatsapp-gateway/internal/store/storedb"
)

// dbExecQuerier is the small subset of *sql.DB / *sql.Tx that the repos need.
// Defining it lets a repo run inside a transaction as well as against a raw DB,
// and keeps the repos easy to satisfy in tests.
type dbExecQuerier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	PrepareContext(ctx context.Context, query string) (*sql.Stmt, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Store aggregates every repository behind one struct for convenient wiring,
// while each repo remains independently constructable via its New<Repo>.
type Store struct {
	db                *sql.DB
	Gateways          *GatewayRepo
	Sessions          *SessionRepo
	APIKeys           *APIKeyRepo
	Webhooks          *WebhookRepo
	WebhookDeliveries *WebhookDeliveryRepo
	Identities        *IdentityRepo
	Contacts          *ContactRepo
	Groups            *GroupRepo
	GroupMembers      *GroupMemberRepo
	Chats             *ChatRepo
	Messages          *MessageRepo
	Polls             *PollRepo
	PollVotes         *PollVoteRepo
	Outbox            *OutboxRepo
	EventLog          *EventLogRepo
	Retention         *RetentionRepo
	BackfillImports   *BackfillImportRepo
	OAuthClients      *OAuthClientRepo
	OAuthGrants       *OAuthGrantRepo
	OAuthRefresh      *OAuthRefreshTokenRepo
	OAuthSigningKeys  *OAuthSigningKeyRepo
	AuditEvents       *AuditRepo
}

// New builds a Store with every repo bound to the same *sql.DB.
func New(db *sql.DB) *Store {
	return &Store{
		db:                db,
		Gateways:          NewGatewayRepo(db),
		Sessions:          NewSessionRepo(db),
		APIKeys:           NewAPIKeyRepo(db),
		Webhooks:          NewWebhookRepo(db),
		WebhookDeliveries: NewWebhookDeliveryRepo(db),
		Identities:        NewIdentityRepo(db),
		Contacts:          NewContactRepo(db),
		Groups:            NewGroupRepo(db),
		GroupMembers:      NewGroupMemberRepo(db),
		Chats:             NewChatRepo(db),
		Messages:          NewMessageRepo(db),
		Polls:             NewPollRepo(db),
		PollVotes:         NewPollVoteRepo(db),
		Outbox:            NewOutboxRepo(db),
		EventLog:          NewEventLogRepo(db),
		Retention:         NewRetentionRepo(db),
		BackfillImports:   NewBackfillImportRepo(db),
		OAuthClients:      NewOAuthClientRepo(db),
		OAuthGrants:       NewOAuthGrantRepo(db),
		OAuthRefresh:      NewOAuthRefreshTokenRepo(db),
		OAuthSigningKeys:  NewOAuthSigningKeyRepo(db),
		AuditEvents:       NewAuditRepo(db),
	}
}

// InTx runs fn with repositories bound to one transaction. It is intended for
// enrollment state transitions that must atomically append an audit event.
func InTx(ctx context.Context, db *sql.DB, fn func(*Store) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	txStore := newWithDBTX(tx)
	if err := fn(txStore); err != nil {
		return err
	}
	return tx.Commit()
}

func newWithDBTX(db storedb.DBTX) *Store {
	return &Store{
		Gateways: NewGatewayRepo(db), Sessions: NewSessionRepo(db), APIKeys: NewAPIKeyRepo(db),
		Webhooks: NewWebhookRepo(db), WebhookDeliveries: NewWebhookDeliveryRepo(db),
		Identities: NewIdentityRepo(db), Contacts: NewContactRepo(db), Groups: NewGroupRepo(db),
		GroupMembers: NewGroupMemberRepo(db), Chats: NewChatRepo(db), Messages: NewMessageRepo(db),
		Polls: NewPollRepo(db), PollVotes: NewPollVoteRepo(db), Outbox: NewOutboxRepo(db),
		EventLog: NewEventLogRepo(db), Retention: NewRetentionRepo(db), BackfillImports: NewBackfillImportRepo(db),
		OAuthClients: NewOAuthClientRepo(db), OAuthGrants: NewOAuthGrantRepo(db),
		OAuthRefresh: NewOAuthRefreshTokenRepo(db), OAuthSigningKeys: NewOAuthSigningKeyRepo(db),
		AuditEvents: NewAuditRepo(db),
	}
}
