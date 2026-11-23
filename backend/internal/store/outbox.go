package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/store/storedb"
)

// OutboxRepo is the repository for outbox (the API's durable send-command
// table, §5/§8). Idempotency is enforced by the unique
// (organization_id, idempotency_key).
//
// State machine: queued → sending (leased: claim_token + lease_until) →
// acknowledged (WhatsApp accepted; projection pending) → sent, or failed /
// unknown. Every write after a claim is fenced by its claim token; a token is
// only replaced by a new claim after the lease expired, so a matching token
// proves no other scheduler took the command.
type OutboxRepo struct {
	db storedb.DBTX
	q  *storedb.Queries
}

// ErrOutboxClaimLost reports a fenced write whose claim token no longer owns
// the command.
var ErrOutboxClaimLost = errors.New("outbox claim lost")

// NewOutboxRepo constructs an OutboxRepo. Batch claims require a concrete
// *sql.DB or caller-owned *sql.Tx so row-lock ownership cannot be ambiguous.
func NewOutboxRepo(db storedb.DBTX) *OutboxRepo { return &OutboxRepo{db: db, q: storedb.New(db)} }

// outboxColumns is the shared shape of every selected outbox row.
type outboxColumns struct {
	ID, OrganizationID, SessionID              string
	IdempotencyKey                             sql.NullString
	Payload                                    json.RawMessage
	Status                                     storedb.OutboxStatus
	DispatchAttempts, Deferrals, ProjectionAtt int32
	NextAttemptAt                              int64
	WaMessageID, Error                         sql.NullString
	TerminalAt                                 sql.NullInt64
	CreatedAt, UpdatedAt                       int64
	ExpiresAt, AcknowledgedAt                  sql.NullInt64
	EffectPossible                             bool
	ClaimToken                                 sql.NullString
	LeaseUntil                                 sql.NullInt64
}

func (c outboxColumns) entry() domain.OutboxEntry {
	o := domain.OutboxEntry{
		ID:                 c.ID,
		OrganizationID:     c.OrganizationID,
		SessionID:          c.SessionID,
		IdempotencyKey:     stringPtrFromNull(c.IdempotencyKey),
		Status:             domain.OutboxStatus(c.Status),
		Attempts:           int(c.DispatchAttempts),
		Deferrals:          int(c.Deferrals),
		ProjectionAttempts: int(c.ProjectionAtt),
		NextAttemptAt:      c.NextAttemptAt,
		ExpiresAt:          int64PtrFromNull(c.ExpiresAt),
		AcknowledgedAt:     int64PtrFromNull(c.AcknowledgedAt),
		EffectPossible:     c.EffectPossible,
		WAMessageID:        stringPtrFromNull(c.WaMessageID),
		Error:              stringPtrFromNull(c.Error),
		TerminalAt:         int64PtrFromNull(c.TerminalAt),
		CreatedAt:          c.CreatedAt,
		UpdatedAt:          c.UpdatedAt,
		ClaimToken:         c.ClaimToken.String,
		LeaseUntil:         c.LeaseUntil.Int64,
	}
	if len(c.Payload) > 0 {
		o.Payload = append([]byte(nil), c.Payload...)
	}
	return o
}

func outboxRowFromGet(r storedb.GetOutboxRow) domain.OutboxEntry {
	return outboxColumns{r.ID, r.OrganizationID, r.SessionID, r.IdempotencyKey, r.Payload, r.Status,
		r.DispatchAttempts, r.Deferrals, r.ProjectionAttempts, r.NextAttemptAt, r.WaMessageID, r.Error,
		r.TerminalAt, r.CreatedAt, r.UpdatedAt, r.ExpiresAt, r.AcknowledgedAt, r.EffectPossible,
		r.ClaimToken, r.LeaseUntil}.entry()
}

func outboxRowFromIdempotency(r storedb.GetOutboxByIdempotencyRow) domain.OutboxEntry {
	return outboxColumns{r.ID, r.OrganizationID, r.SessionID, r.IdempotencyKey, r.Payload, r.Status,
		r.DispatchAttempts, r.Deferrals, r.ProjectionAttempts, r.NextAttemptAt, r.WaMessageID, r.Error,
		r.TerminalAt, r.CreatedAt, r.UpdatedAt, r.ExpiresAt, r.AcknowledgedAt, r.EffectPossible,
		r.ClaimToken, r.LeaseUntil}.entry()
}

func outboxRowFromDueClaim(r storedb.SelectDueOutboxForClaimRow) domain.OutboxEntry {
	return outboxColumns{r.ID, r.OrganizationID, r.SessionID, r.IdempotencyKey, r.Payload, r.Status,
		r.DispatchAttempts, r.Deferrals, r.ProjectionAttempts, r.NextAttemptAt, r.WaMessageID, r.Error,
		r.TerminalAt, r.CreatedAt, r.UpdatedAt, r.ExpiresAt, r.AcknowledgedAt, r.EffectPossible,
		r.ClaimToken, r.LeaseUntil}.entry()
}

// Insert appends a command. A sync send inserts it already leased (status
// sending with ClaimToken and LeaseUntil); an async send inserts it queued.
// The unique (organization_id, idempotency_key) makes a duplicate key a
// conflict the caller resolves via GetByIdempotency (§8 replay semantics).
func (r *OutboxRepo) Insert(ctx context.Context, o domain.OutboxEntry) error {
	err := r.q.InsertOutbox(ctx, storedb.InsertOutboxParams{
		ID:               o.ID,
		OrganizationID:   o.OrganizationID,
		SessionID:        o.SessionID,
		IdempotencyKey:   nullString(o.IdempotencyKey),
		Payload:          o.Payload,
		Status:           storedb.OutboxStatus(o.Status),
		DispatchAttempts: int32(o.Attempts),
		NextAttemptAt:    o.NextAttemptAt,
		WaMessageID:      nullString(o.WAMessageID),
		Error:            nullString(o.Error),
		TerminalAt:       nullInt64(o.TerminalAt),
		CreatedAt:        o.CreatedAt,
		UpdatedAt:        o.UpdatedAt,
		ExpiresAt:        nullInt64(o.ExpiresAt),
		ClaimToken:       sql.NullString{String: o.ClaimToken, Valid: o.ClaimToken != ""},
		LeaseUntil:       sql.NullInt64{Int64: o.LeaseUntil, Valid: o.ClaimToken != ""},
	})
	if err != nil {
		return fmt.Errorf("store: insert outbox: %w", err)
	}
	return nil
}

// Get fetches an outbox entry by id. Maps no-rows to not_found.
func (r *OutboxRepo) Get(ctx context.Context, id string) (domain.OutboxEntry, error) {
	row, err := r.q.GetOutbox(ctx, storedb.GetOutboxParams{ID: id})
	if err != nil {
		return domain.OutboxEntry{}, notFound(err, "outbox entry")
	}
	return outboxRowFromGet(row), nil
}

// GetByIdempotency returns the prior entry for (organization_id, idempotency_key) — the
// §8 idempotent replay lookup. Maps no-rows to not_found so the caller knows to
// proceed with a fresh send.
func (r *OutboxRepo) GetByIdempotency(ctx context.Context, organizationID, idempotencyKey string) (domain.OutboxEntry, error) {
	row, err := r.q.GetOutboxByIdempotency(ctx, storedb.GetOutboxByIdempotencyParams{
		OrganizationID: organizationID,
		IdempotencyKey: sqlString(idempotencyKey),
	})
	if err != nil {
		return domain.OutboxEntry{}, notFound(err, "outbox entry")
	}
	return outboxRowFromIdempotency(row), nil
}

// OutboxClaim is one claim attempt: every claimed row gets Token and a lease
// until LeaseUntil. Now is the eligibility cutoff.
type OutboxClaim struct {
	Token      string
	Now        int64
	LeaseUntil int64
	Limit      int
}

// ClaimDue leases up to Limit due commands: queued commands and acknowledged
// projections that are due (or past expiry), plus sending commands whose lease
// expired. Selection uses FOR UPDATE SKIP LOCKED and every row is leased in
// the same transaction, so concurrent schedulers receive disjoint batches.
// Rows are returned in scheduled order (oldest-due first).
func (r *OutboxRepo) ClaimDue(ctx context.Context, c OutboxClaim) ([]domain.OutboxEntry, error) {
	if c.Token == "" || c.LeaseUntil <= c.Now {
		return nil, fmt.Errorf("store: outbox claim needs a token and a lease after now")
	}
	tx, commit, rollback, err := r.begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: begin claim due outbox: %w", err)
	}
	defer func() { _ = rollback() }()
	q := storedb.New(tx)
	rows, err := q.SelectDueOutboxForClaim(ctx, storedb.SelectDueOutboxForClaimParams{
		Now:          c.Now,
		ExpiryBefore: sql.NullInt64{Int64: c.Now, Valid: true},
		LeaseBefore:  sql.NullInt64{Int64: c.Now, Valid: true},
		Limit:        int32(normLimit(c.Limit)),
	})
	if err != nil {
		return nil, fmt.Errorf("store: select due outbox: %w", err)
	}
	out := make([]domain.OutboxEntry, 0, len(rows))
	for _, row := range rows {
		entry := outboxRowFromDueClaim(row)
		leased, err := r.lease(ctx, q, entry.ID, c)
		if err != nil {
			return nil, err
		}
		if leased {
			entry.Status = domain.OutboxSending
			entry.ClaimToken, entry.LeaseUntil, entry.UpdatedAt = c.Token, c.LeaseUntil, c.Now
			out = append(out, entry)
		}
	}
	if err := commit(); err != nil {
		return nil, fmt.Errorf("store: commit claim due outbox: %w", err)
	}
	return out, nil
}

// ClaimByID leases one command if it is claimable (queued, acknowledged, or
// sending with an expired lease), regardless of its due time.
func (r *OutboxRepo) ClaimByID(ctx context.Context, id string, c OutboxClaim) (bool, error) {
	return r.lease(ctx, r.q, id, c)
}

func (r *OutboxRepo) lease(ctx context.Context, q *storedb.Queries, id string, c OutboxClaim) (bool, error) {
	n, err := q.LeaseOutbox(ctx, storedb.LeaseOutboxParams{
		ClaimToken:  sqlString(c.Token),
		LeaseUntil:  sql.NullInt64{Int64: c.LeaseUntil, Valid: true},
		Now:         c.Now,
		ID:          id,
		LeaseBefore: sql.NullInt64{Int64: c.Now, Valid: true},
	})
	if err != nil {
		return false, fmt.Errorf("store: lease outbox %s: %w", id, err)
	}
	return n == 1, nil
}

func (r *OutboxRepo) begin(ctx context.Context) (storedb.DBTX, func() error, func() error, error) {
	if tx, ok := r.db.(*sql.Tx); ok {
		return tx, func() error { return nil }, func() error { return nil }, nil
	}
	db, ok := r.db.(*sql.DB)
	if !ok {
		return nil, nil, nil, fmt.Errorf("store: claim due outbox requires *sql.DB or *sql.Tx")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, nil, err
	}
	return tx, tx.Commit, tx.Rollback, nil
}

func fencedOutbox(n int64, err error, what, id string) error {
	if err != nil {
		return fmt.Errorf("store: %s outbox %s: %w", what, id, err)
	}
	if n != 1 {
		return ErrOutboxClaimLost
	}
	return nil
}

// RenewLease extends the lease of a sending command this claim owns.
func (r *OutboxRepo) RenewLease(ctx context.Context, id, token string, leaseUntil int64) error {
	n, err := r.q.RenewOutboxLease(ctx, storedb.RenewOutboxLeaseParams{
		LeaseUntil: sql.NullInt64{Int64: leaseUntil, Valid: true}, ID: id, ClaimToken: sqlString(token),
	})
	return fencedOutbox(n, err, "renew lease of", id)
}

// MarkDispatch runs before every engine dispatch of a command that has no
// acknowledgement yet: it records that a remote effect may happen, counts the
// dispatch attempt and renews the lease.
func (r *OutboxRepo) MarkDispatch(ctx context.Context, id, token string, leaseUntil int64) error {
	n, err := r.q.MarkOutboxDispatch(ctx, storedb.MarkOutboxDispatchParams{
		LeaseUntil: sql.NullInt64{Int64: leaseUntil, Valid: true}, ID: id, ClaimToken: sqlString(token),
	})
	return fencedOutbox(n, err, "mark dispatch of", id)
}

// ClearDispatchPossible is valid only after the engine client proves it
// returned before issuing an RPC and the row had no earlier possible effect.
func (r *OutboxRepo) ClearDispatchPossible(ctx context.Context, id, token string) error {
	n, err := r.q.ClearOutboxDispatchPossible(ctx, storedb.ClearOutboxDispatchPossibleParams{
		ID: id, ClaimToken: sqlString(token),
	})
	return fencedOutbox(n, err, "clear dispatch of", id)
}

// RecordAcknowledgement persists a definite remote commit before any local
// projection. Recovery therefore never lets a later expiry erase success.
func (r *OutboxRepo) RecordAcknowledgement(ctx context.Context, id, token, waID string, acknowledgedAt int64) error {
	n, err := r.q.RecordOutboxAcknowledgement(ctx, storedb.RecordOutboxAcknowledgementParams{
		WaMessageID:    sqlString(waID),
		AcknowledgedAt: sql.NullInt64{Int64: acknowledgedAt, Valid: true},
		ID:             id, ClaimToken: sqlString(token),
	})
	return fencedOutbox(n, err, "acknowledge", id)
}

// Defer returns an unacknowledged command to queued without counting a
// dispatch attempt (rate limit, or a pre-dispatch infrastructure check).
func (r *OutboxRepo) Defer(ctx context.Context, id, token, note string, nextAttemptAt, now int64) error {
	n, err := r.q.DeferOutbox(ctx, storedb.DeferOutboxParams{
		Error: sqlString(note), NextAttemptAt: nextAttemptAt, UpdatedAt: now, ID: id, ClaimToken: sqlString(token),
	})
	return fencedOutbox(n, err, "defer", id)
}

// Retry returns an unacknowledged command to queued after an ambiguous
// dispatch attempt (already counted by MarkDispatch).
func (r *OutboxRepo) Retry(ctx context.Context, id, token, note string, nextAttemptAt, now int64) error {
	n, err := r.q.RetryOutbox(ctx, storedb.RetryOutboxParams{
		Error: sqlString(note), NextAttemptAt: nextAttemptAt, UpdatedAt: now, ID: id, ClaimToken: sqlString(token),
	})
	return fencedOutbox(n, err, "retry", id)
}

// DeferProjection parks an acknowledged command whose projection failed and
// counts the projection attempt.
func (r *OutboxRepo) DeferProjection(ctx context.Context, id, token, note string, nextAttemptAt, now int64) error {
	n, err := r.q.DeferOutboxProjection(ctx, storedb.DeferOutboxProjectionParams{
		Error: sqlString(note), NextAttemptAt: nextAttemptAt, UpdatedAt: now, ID: id, ClaimToken: sqlString(token),
	})
	return fencedOutbox(n, err, "defer projection of", id)
}

// CompleteOwned settles a command terminally (sent, failed or unknown), fenced
// by its claim token. A sent command's inline media bytes are stripped.
func (r *OutboxRepo) CompleteOwned(ctx context.Context, id, token string, status domain.OutboxStatus, waID, note *string, updatedAt int64) error {
	n, err := r.q.CompleteOwnedOutbox(ctx, storedb.CompleteOwnedOutboxParams{
		ID: id, ClaimToken: sqlString(token), FinalStatus: storedb.OutboxStatus(status),
		WaMessageID: nullString(waID), Error: nullString(note),
		TerminalAt: sql.NullInt64{Int64: updatedAt, Valid: true}, UpdatedAt: updatedAt,
	})
	return fencedOutbox(n, err, "complete", id)
}
