package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rama-adi/quick-whatsapp-gateway/internal/domain"
	"github.com/rama-adi/quick-whatsapp-gateway/internal/store/storedb"
)

// Fan-out sources. Gateway events are projected and then published; API
// events (sent messages) were already projected by their producer, so fan-out
// only publishes them.
const (
	FanoutSourceGateway = "gateway"
	FanoutSourceAPI     = "api"
)

// Fan-out work statuses (event_fanout.status).
const (
	FanoutPending = "pending"
	FanoutLeased  = "leased"
	FanoutDone    = "done"
	FanoutDead    = "dead"
)

// ErrFanoutClaimLost reports that a fenced fan-out write found another claim
// token (or no lease) on the row: a newer claim owns it now.
var ErrFanoutClaimLost = errors.New("committed event claim lost")

// maxFanoutErrorBytes bounds last_error so one huge error cannot bloat a row.
const maxFanoutErrorBytes = 2048

type fanoutItem struct {
	eventID, organizationID, sessionID, source string
	createdAt                                  int64
}

// enqueueFanout assigns the next per-session sequence and records pending
// fan-out work, inside the caller's committing transaction. The sequence row
// lock serializes concurrent writers of one session until commit, so
// session_seq order is commit order.
func enqueueFanout(ctx context.Context, tx storedb.DBTX, item fanoutItem) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO session_event_sequences (session_id, last_seq) VALUES (?, 1)
		 ON DUPLICATE KEY UPDATE last_seq = last_seq + 1`, item.sessionID); err != nil {
		return fmt.Errorf("store: advance session event sequence: %w", err)
	}
	var seq uint64
	if err := tx.QueryRowContext(ctx,
		`SELECT last_seq FROM session_event_sequences WHERE session_id=?`, item.sessionID).Scan(&seq); err != nil {
		return fmt.Errorf("store: read session event sequence: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO event_fanout
		 (event_id, organization_id, session_id, session_seq, source, status, attempts, next_attempt_at, created_at)
		 VALUES (?,?,?,?,?,'pending',0,?,?)`,
		item.eventID, item.organizationID, item.sessionID, seq, item.source, item.createdAt, item.createdAt); err != nil {
		return fmt.Errorf("store: enqueue event fan-out %s: %w", item.eventID, err)
	}
	return nil
}

// FanoutClaim is one multi-replica-safe claim attempt. Token is unique per
// claim and fences every later write; Owner is diagnostic only.
type FanoutClaim struct {
	Owner         string
	Token         string
	ClaimedAt     time.Time
	LeaseUntil    time.Time
	MaxSessions   int
	MaxPerSession int
}

// ClaimedFanout is one leased event, in session_seq order within its session.
type ClaimedFanout struct {
	Event      domain.Event
	Source     string
	SessionSeq uint64
	Attempts   int // attempts started before this claim
	LastError  string
	// Missing is true when the event body is no longer in event_log; the
	// event can never be fanned out and should be dead-lettered.
	Missing bool
}

// FanoutFailure records one failed attempt. Dead moves the row to the
// dead-letter state (unblocking its session); otherwise it returns to pending
// with NextAttemptAt.
type FanoutFailure struct {
	Token, EventID, Error string
	Dead                  bool
	NextAttemptAt, At     time.Time
}

// FanoutBacklog summarizes outstanding and dead-lettered fan-out work.
type FanoutBacklog struct {
	Outstanding     int64
	Dead            int64
	OldestEventID   string
	OldestSessionID string
	OldestCreatedAt int64
	OldestAttempts  int
	OldestLastError string
}

// EventFanoutRepo owns the event_fanout work table.
type EventFanoutRepo struct{ db storedb.DBTX }

func NewEventFanoutRepo(db storedb.DBTX) *EventFanoutRepo { return &EventFanoutRepo{db: db} }

func (r *EventFanoutRepo) begin(ctx context.Context) (storedb.DBTX, func() error, func() error, error) {
	if tx, ok := r.db.(*sql.Tx); ok {
		return tx, func() error { return nil }, func() error { return nil }, nil
	}
	db, ok := r.db.(*sql.DB)
	if !ok {
		return nil, nil, nil, fmt.Errorf("store: event fan-out requires *sql.DB or *sql.Tx")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, nil, err
	}
	return tx, tx.Commit, tx.Rollback, nil
}

// Claim leases the head of up to MaxSessions sessions plus up to
// MaxPerSession-1 contiguous successors each. A session's head is its lowest
// pending/leased session_seq; it is claimable only when due and not held by a
// live lease, so one session is never processed by two claims at once and a
// session waits behind its head until the head is done or dead.
func (r *EventFanoutRepo) Claim(ctx context.Context, c FanoutClaim) ([]ClaimedFanout, error) {
	if c.Token == "" || c.Owner == "" {
		return nil, fmt.Errorf("store: fan-out claim token and owner are required")
	}
	if c.MaxSessions <= 0 || c.MaxPerSession <= 0 {
		return nil, fmt.Errorf("store: fan-out claim sizes must be positive")
	}
	if c.ClaimedAt.IsZero() || !c.LeaseUntil.After(c.ClaimedAt) {
		return nil, fmt.Errorf("store: fan-out lease must end after claim time")
	}
	tx, commit, rollback, err := r.begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: begin fan-out claim: %w", err)
	}
	defer func() { _ = rollback() }()
	now := c.ClaimedAt.UnixMilli()

	type row struct {
		eventID, sessionID, source, lastError string
		seq                                   uint64
		attempts                              int
	}
	scan := func(rows *sql.Rows) ([]row, error) {
		defer func() { _ = rows.Close() }()
		var out []row
		for rows.Next() {
			var x row
			var lastErr sql.NullString
			if err := rows.Scan(&x.eventID, &x.sessionID, &x.seq, &x.source, &x.attempts, &lastErr); err != nil {
				return nil, err
			}
			x.lastError = lastErr.String
			out = append(out, x)
		}
		return out, rows.Err()
	}
	// The derived head table is a consistent read; the outer rows are locking
	// (current) reads. A stale snapshot can only name an older head, which
	// then fails the outer predicate, so the race is conservative.
	rows, err := tx.QueryContext(ctx,
		`SELECT f.event_id, f.session_id, f.session_seq, f.source, f.attempts, f.last_error
		 FROM event_fanout f
		 JOIN (SELECT session_id, MIN(session_seq) AS head FROM event_fanout
		       WHERE status IN ('pending','leased') GROUP BY session_id) h
		   ON h.session_id = f.session_id AND h.head = f.session_seq
		 WHERE f.status IN ('pending','leased') AND (f.status = 'pending' OR f.lease_until <= ?)
		   AND f.next_attempt_at <= ?
		 ORDER BY f.next_attempt_at, f.session_id
		 LIMIT ? FOR UPDATE OF f SKIP LOCKED`,
		now, now, c.MaxSessions)
	if err != nil {
		return nil, fmt.Errorf("store: select fan-out heads: %w", err)
	}
	heads, err := scan(rows)
	if err != nil {
		return nil, fmt.Errorf("store: scan fan-out heads: %w", err)
	}
	var claimed []row
	for _, head := range heads {
		claimed = append(claimed, head)
		if c.MaxPerSession == 1 {
			continue
		}
		rows, err := tx.QueryContext(ctx,
			`SELECT event_id, session_id, session_seq, source, attempts, last_error FROM event_fanout
			 WHERE session_id = ? AND session_seq > ? AND status IN ('pending','leased')
			   AND (status = 'pending' OR lease_until <= ?)
			 ORDER BY session_seq LIMIT ? FOR UPDATE`,
			head.sessionID, head.seq, now, c.MaxPerSession-1)
		if err != nil {
			return nil, fmt.Errorf("store: select fan-out successors: %w", err)
		}
		next, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan fan-out successors: %w", err)
		}
		prev := head.seq
		for _, x := range next {
			if x.seq != prev+1 {
				break // a live-leased or missing successor ends the contiguous run
			}
			claimed = append(claimed, x)
			prev = x.seq
		}
	}
	if len(claimed) == 0 {
		return nil, commit()
	}
	ids := make([]any, 0, len(claimed)+3)
	ids = append(ids, c.Token, c.Owner, c.LeaseUntil.UnixMilli())
	marks := make([]string, 0, len(claimed))
	for _, x := range claimed {
		ids = append(ids, x.eventID)
		marks = append(marks, "?")
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE event_fanout SET status='leased', claim_token=?, claimed_by=?, lease_until=?
		 WHERE event_id IN (`+strings.Join(marks, ",")+`)`, ids...); err != nil {
		return nil, fmt.Errorf("store: lease fan-out work: %w", err)
	}
	bodies, err := tx.QueryContext(ctx,
		`SELECT event_id, type, organization_id, session_id, created_at, payload FROM event_log
		 WHERE event_id IN (`+strings.Join(marks, ",")+`)`, ids[3:]...)
	if err != nil {
		return nil, fmt.Errorf("store: load fan-out event bodies: %w", err)
	}
	events := map[string]domain.Event{}
	for bodies.Next() {
		var e domain.Event
		var payload []byte
		if err := bodies.Scan(&e.ID, &e.Type, &e.Organization, &e.Session, &e.Timestamp, &payload); err != nil {
			_ = bodies.Close()
			return nil, fmt.Errorf("store: scan fan-out event body: %w", err)
		}
		e.Schema = domain.Schema
		e.Payload = json.RawMessage(payload)
		events[e.ID] = e
	}
	if err := bodies.Err(); err != nil {
		_ = bodies.Close()
		return nil, fmt.Errorf("store: iterate fan-out event bodies: %w", err)
	}
	_ = bodies.Close()
	if err := commit(); err != nil {
		return nil, fmt.Errorf("store: commit fan-out claim: %w", err)
	}
	out := make([]ClaimedFanout, 0, len(claimed))
	for _, x := range claimed {
		e, ok := events[x.eventID]
		if !ok {
			e = domain.Event{Schema: domain.Schema, ID: x.eventID, Session: x.sessionID}
		}
		out = append(out, ClaimedFanout{
			Event: e, Source: x.source, SessionSeq: x.seq, Attempts: x.attempts, LastError: x.lastError, Missing: !ok,
		})
	}
	return out, nil
}

// Start records that one attempt begins: attempts++ and the lease renews for
// this event. Counting at start (not at failure) means a worker that crashes
// while processing an event still uses up an attempt.
func (r *EventFanoutRepo) Start(ctx context.Context, token, eventID string, leaseUntil time.Time) error {
	return r.fenced(ctx, `UPDATE event_fanout SET attempts = attempts + 1, lease_until = ?
		WHERE event_id = ? AND claim_token = ? AND status = 'leased'`, leaseUntil.UnixMilli(), eventID, token)
}

// Complete marks one event done. It is fenced by the claim token: a claim is
// only ever replaced after its lease expired, so a matching token proves no
// other claim has taken the event.
func (r *EventFanoutRepo) Complete(ctx context.Context, token, eventID string, at time.Time) error {
	return r.fenced(ctx, `UPDATE event_fanout SET status = 'done', finished_at = ?, claim_token = NULL,
		lease_until = NULL, last_error = NULL WHERE event_id = ? AND claim_token = ? AND status = 'leased'`,
		at.UnixMilli(), eventID, token)
}

// Fail records a failed attempt, either retryable (back to pending) or dead.
func (r *EventFanoutRepo) Fail(ctx context.Context, f FanoutFailure) error {
	msg := f.Error
	if len(msg) > maxFanoutErrorBytes {
		msg = msg[:maxFanoutErrorBytes]
	}
	if f.Dead {
		return r.fenced(ctx, `UPDATE event_fanout SET status = 'dead', finished_at = ?, last_error = ?,
			claim_token = NULL, lease_until = NULL WHERE event_id = ? AND claim_token = ? AND status = 'leased'`,
			f.At.UnixMilli(), msg, f.EventID, f.Token)
	}
	return r.fenced(ctx, `UPDATE event_fanout SET status = 'pending', next_attempt_at = ?, last_error = ?,
		claim_token = NULL, lease_until = NULL WHERE event_id = ? AND claim_token = ? AND status = 'leased'`,
		f.NextAttemptAt.UnixMilli(), msg, f.EventID, f.Token)
}

// Release returns a claim's untouched rows of one session to pending without
// counting an attempt (they were never started).
func (r *EventFanoutRepo) Release(ctx context.Context, token, sessionID string) error {
	if _, err := r.db.ExecContext(ctx, `UPDATE event_fanout SET status = 'pending', claim_token = NULL,
		lease_until = NULL WHERE claim_token = ? AND session_id = ? AND status = 'leased'`, token, sessionID); err != nil {
		return fmt.Errorf("store: release fan-out work: %w", err)
	}
	return nil
}

func (r *EventFanoutRepo) fenced(ctx context.Context, query string, args ...any) error {
	res, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("store: update fan-out work: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrFanoutClaimLost
	}
	return nil
}

// Backlog reports outstanding and dead fan-out work plus the oldest
// outstanding event, for operator logs.
func (r *EventFanoutRepo) Backlog(ctx context.Context) (FanoutBacklog, error) {
	var b FanoutBacklog
	if err := r.db.QueryRowContext(ctx,
		`SELECT (SELECT COUNT(*) FROM event_fanout WHERE status IN ('pending','leased')),
		        (SELECT COUNT(*) FROM event_fanout WHERE status = 'dead')`).Scan(&b.Outstanding, &b.Dead); err != nil {
		return b, fmt.Errorf("store: fan-out backlog: %w", err)
	}
	if b.Outstanding == 0 {
		return b, nil
	}
	var lastErr sql.NullString
	err := r.db.QueryRowContext(ctx,
		`SELECT event_id, session_id, created_at, attempts, last_error FROM event_fanout
		 WHERE status IN ('pending','leased') ORDER BY created_at, event_id LIMIT 1`).
		Scan(&b.OldestEventID, &b.OldestSessionID, &b.OldestCreatedAt, &b.OldestAttempts, &lastErr)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return b, fmt.Errorf("store: oldest fan-out work: %w", err)
	}
	b.OldestLastError = lastErr.String
	return b, nil
}

// Prune deletes done fan-out rows finished before cutoffMs, and dead rows
// finished before deadCutoffMs, in bounded batches.
func (r *EventFanoutRepo) Prune(ctx context.Context, cutoffMs, deadCutoffMs int64) (int64, error) {
	var total int64
	for _, step := range []struct {
		status string
		cutoff int64
	}{{FanoutDone, cutoffMs}, {FanoutDead, deadCutoffMs}} {
		n, err := deleteBatches(func() (int64, error) {
			res, err := r.db.ExecContext(ctx, `DELETE FROM event_fanout WHERE status = ? AND finished_at < ?
				ORDER BY finished_at LIMIT ?`, step.status, step.cutoff, retentionDeleteBatchSize)
			if err != nil {
				return 0, fmt.Errorf("store: prune event fan-out: %w", err)
			}
			return res.RowsAffected()
		})
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
